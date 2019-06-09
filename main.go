package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"

	_ "bazil.org/fuse/fs/fstestutil"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s MOUNTPOINT\n", os.Args[0])
	flag.PrintDefaults()
}

func run(ctx context.Context, mountpoint string) (error, chan error) {
	c, filesys, err := start(mountpoint)
	if err != nil {
		return err, nil
	}

	srv := fs.New(c, nil)

	var ret = make(chan error)
	go func() {
		ret <- srv.Serve(filesys)
		_ = fuse.Unmount(mountpoint)
		_ = c.Close()
	}()

	// When context expires, close conn, which will stop srv.Serve
	go func() {
		<-ctx.Done()
		_ = fuse.Unmount(mountpoint)
		_ = c.Close()
	}()

	<-c.Ready
	return c.MountError, ret
}

func start(mountpoint string) (*fuse.Conn, *FS, error) {
	c, err := fuse.Mount(
		mountpoint,
		fuse.FSName("vaultfs"),
		fuse.Subtype("vaultfs"),
		fuse.LocalVolume(),
		fuse.VolumeName("Vault filesystem"),
	)
	if err != nil {
		return nil, nil, err
	}

	filesys, err := NewFS()
	if err != nil {
		_ = fuse.Unmount(mountpoint)
		_ = c.Close()
		return nil, nil, err
	}

	return c, filesys, nil
}

var debug bool

func main() {
	var (
		flagDebug     = flag.Bool("debug", false, "enable debugging")
		flagDebugFuse = flag.Bool("debugfuse", false, "enable FUSE debugging")
	)
	flag.Usage = usage
	flag.Parse()
	if *flagDebug {
		debug = true
	}
	if *flagDebugFuse {
		fuse.Debug = func(msg interface{}) {
			log.Println(msg)
		}
	}

	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	mountpoint := flag.Arg(0)

	err, cerr := run(context.Background(), mountpoint)
	if err != nil {
		log.Fatal(err)
	}
	err = <-cerr
	if err != nil {
		log.Fatal(err)
	}
}
