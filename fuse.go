package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"bazil.org/fuse/fuseutil"
	"github.com/hashicorp/vault/api"
)

type FS struct {
	client *vaultapi
}

func NewFS() (*FS, error) {
	client, err := api.NewClient(nil)
	if err != nil {
		return nil, err
	}

	return &FS{
		client: &vaultapi{client},
	}, nil
}

var _ fs.FS = (*FS)(nil)

func (f *FS) Root() (fs.Node, error) {
	mounts, err := f.client.Sys().ListMounts()
	if err != nil {
		return nil, err
	}
	return &RootDir{
		fs:     f,
		mounts: mounts,
	}, nil
}

// RootDir implements both Node and Handle for the root directory.
type RootDir struct {
	fs *FS
	// mounts maps mountpoint (including trailing slash) to mount entry
	mounts map[string]*api.MountOutput
}

var _ fs.Node = (*RootDir)(nil)

func (d *RootDir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Mode = os.ModeDir | 0555
	return nil
}

var _ fs.NodeStringLookuper = (*RootDir)(nil)

type nodeMaker func(*FS, string, *api.MountOutput) (*MountDir, error)

var nodeMakers = map[string]nodeMaker{
	"kv": makeKvNode,
}

func (d *RootDir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	mount := d.mounts[name+"/"]
	if mount == nil {
		return nil, fmt.Errorf("no such mount: %q", name)
	}
	maker := nodeMakers[mount.Type]
	if maker == nil {
		return newFile(""), nil
	}
	return maker(d.fs, name, mount)
}

func makeKvNode(f *FS, mountpt string, mount *api.MountOutput) (*MountDir, error) {
	var adj pathAdjustor = basePathAdjustor{}
	if mount.Options["version"] == "2" {
		adj = kvv2PathAdjustor{}
	}
	return &MountDir{
		fs:           f,
		mountpt:      mountpt,
		mount:        mount,
		pathAdjustor: adj,
	}, nil
}

var _ fs.HandleReadDirAller = (*RootDir)(nil)

func (d *RootDir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	dirs := make([]fuse.Dirent, 0, len(d.mounts))
	for mntpt := range d.mounts {
		dirs = append(dirs, fuse.Dirent{
			Name: strings.TrimSuffix(mntpt, "/"),
			Type: fuse.DT_Dir,
		})
	}
	return dirs, nil
}

type pathAdjustor interface {
	pathlist(in string) string
	pathread(in string) string
}

type basePathAdjustor struct{}

func (a basePathAdjustor) pathlist(in string) string {
	return in
}
func (a basePathAdjustor) pathread(in string) string {
	return in
}

var _ pathAdjustor = basePathAdjustor{}

type kvv2PathAdjustor struct{}

func (a kvv2PathAdjustor) pathlist(in string) string {
	return filepath.Join("metadata", in)
}
func (a kvv2PathAdjustor) pathread(in string) string {
	return filepath.Join("data", in)
}

var _ pathAdjustor = kvv2PathAdjustor{}

func list(ctx context.Context, client *vaultapi, path string) ([]string, error) {
	sec, err := client.Logical().List(path)
	if sec == nil || err != nil {
		return nil, err
	}
	listRaw := sec.Data["keys"]
	if listRaw == nil {
		return nil, nil
	}
	list := listRaw.([]interface{})
	ss := make([]string, len(list))
	for i, l := range list {
		ss[i] = l.(string)
	}
	return ss, nil
}

func listDirents(ctx context.Context, client *vaultapi, path string) ([]fuse.Dirent, error) {
	ss, err := list(ctx, client, path)
	if err != nil {
		return nil, err
	}
	dirs := make([]fuse.Dirent, len(ss))
	for i, s := range ss {
		if strings.HasSuffix(s, "/") {
			dirs[i] = fuse.Dirent{
				Name: s,
				Type: fuse.DT_Dir,
			}
		} else {
			dirs[i] = fuse.Dirent{
				Name: s,
				Type: fuse.DT_File,
			}
		}
	}
	return dirs, nil
}

type MountDir struct {
	fs      *FS
	mountpt string
	mount   *api.MountOutput
	pathAdjustor
}

var _ fs.Node = (*MountDir)(nil)

func (d *MountDir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Mode = os.ModeDir | 0555
	return nil
}

var _ fs.NodeStringLookuper = (*MountDir)(nil)

func (d *MountDir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	return listDirents(ctx, d.fs.client, filepath.Join(d.mountpt, d.pathlist("")))
}

func (d *MountDir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	return lookup(ctx, d, "", name)
}

func lookup(ctx context.Context, d *MountDir, relpath, name string) (fs.Node, error) {
	childpath := filepath.Join(relpath, name)
	// List parent to determine whether a dir or file.  We don't support
	// the case where both "foo" and "foo/" exist.
	ss, err := list(ctx, d.fs.client, filepath.Join(d.mountpt, d.pathlist(relpath)))
	if err != nil {
		return nil, err
	}
	asDir := name + "/"
	for _, s := range ss {
		switch s {
		case asDir:
			return &Dir{
				MountDir: d,
				path:     childpath,
			}, nil
		case name:
			path := filepath.Join(d.mountpt, d.pathread(childpath))
			sec, err := d.fs.client.Logical().Read(path)
			if err != nil {
				return nil, err
			}

			data := sec.Data
			if d.mount.Type == "kv" && d.mount.Options["version"] == "2" {
				data = data["data"].(map[string]interface{})
			}
			b, err := json.Marshal(data)
			if err != nil {
				return nil, err
			}
			return newFile(string(b)), nil
		}
	}

	return nil, fmt.Errorf("not found")
}

type Dir struct {
	*MountDir
	path string
}

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	return listDirents(ctx, d.fs.client, d.pathlist(filepath.Join(d.mountpt, d.path)))
}

func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	return lookup(ctx, d.MountDir, d.path, name)
}

var _ fs.Node = (*Dir)(nil)

var _ fs.NodeStringLookuper = (*Dir)(nil)

func newFile(content string) *File {
	f := &File{}
	f.content.Store(content)
	return f
}

type File struct {
	content atomic.Value
}

var _ fs.Node = (*File)(nil)

func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Mode = 0444
	t := f.content.Load().(string)
	a.Size = uint64(len(t))
	return nil
}

var _ fs.NodeOpener = (*File)(nil)

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	if !req.Flags.IsReadOnly() {
		return nil, fuse.Errno(syscall.EACCES)
	}
	resp.Flags |= fuse.OpenKeepCache
	return f, nil
}

var _ fs.Handle = (*File)(nil)

var _ fs.HandleReader = (*File)(nil)

func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	t := f.content.Load().(string)
	fuseutil.HandleRead(req, resp, []byte(t))
	return nil
}
