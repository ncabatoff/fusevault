package main

import (
	"context"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/vault/api"
)

type vault struct {
	output io.ReadCloser
	cmd    *exec.Cmd
	api    *vaultapi
}

func (v *vault) close() (output string, err error) {
	if v.output != nil {
		b, _ := ioutil.ReadAll(v.output)
		output = string(b)
	}
	err = v.cmd.Wait()
	return string(output), err
}

func devvault(t *testing.T, ctx context.Context) (*vault, error) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	err = listener.Close()
	if err != nil {
		t.Fatal(err)
	}

	roottoken := "devroot"
	err = os.Setenv("VAULT_TOKEN", roottoken)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Setenv("VAULT_ADDR", "http://"+addr)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, "vault", "server", "-dev",
		"-dev-root-token-id="+roottoken, "-dev-listen-address="+addr)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	err = cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("running vault %v", cmd.Args)

	cfg := api.DefaultConfig()
	cfg.Timeout = 1 * time.Second
	client, err := api.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	v := &vault{
		output: output,
		cmd:    cmd,
		api: &vaultapi{
			Client: client,
		},
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			v.api = nil
			return v, ctx.Err()
		case <-ticker.C:
			_, err := client.Sys().ListMounts()
			if err != nil {
				t.Log(err)
				continue
			}
			return v, nil
		}
	}
}

const setupTimeout = 30 * time.Second

func setup(t *testing.T, vaultsetup func(*api.Client) error) (string, *vaultapi, func()) {
	dir, err := ioutil.TempDir("", "vaultfuse")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(setupTimeout))

	v, err := devvault(t, ctx)
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}

	cleanup := func() {
		cancel()
		_ = os.RemoveAll(dir)
		output, err := v.close()
		if err.Error() != "signal: killed" {
			t.Logf("err=%v api=%v output=%v", err, v.api, output)
		}
	}

	if vaultsetup != nil {
		err = vaultsetup(v.api.Client)
		if err != nil {
			cleanup()
			t.Fatalf("error setting up vault: %v", err)
		}
	}

	err, cerr := run(ctx, dir)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}

	srvcleanup := func() {
		cleanup()
		err := <-cerr
		if err != nil {
			log.Fatal(err)
		}
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			srvcleanup()
			t.Fatal("readdir never stabilized")
		case <-ticker.C:
			_, err := ioutil.ReadDir(dir)
			if err == nil {
				return dir, v.api, srvcleanup
			}
		}
	}

	return dir, v.api, srvcleanup
}

func readents(t *testing.T, path string) []string {
	t.Helper()

	ents, err := ioutil.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}

	filtnames := make([]string, 0, len(ents))
	for _, ent := range ents {
		// Skip special files like .fsventsd
		if !strings.HasPrefix(ent.Name(), ".") {
			filtnames = append(filtnames, ent.Name())
		}
	}

	sort.Strings(filtnames)
	return filtnames
}

func TestMount(t *testing.T) {
	dir, _, cleanup := setup(t, nil)
	defer cleanup()

	defaultMounts := []string{"cubbyhole", "identity", "secret", "sys"}
	if diff := cmp.Diff(readents(t, dir), defaultMounts); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}
}

func vwrite(t *testing.T, client *vaultapi, path string, data map[string]interface{}) *api.Secret {
	t.Helper()

	sec, err := client.Logical().Write(path, data)
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

func TestKVV1(t *testing.T) {
	kv := "kvv1"
	dir, client, cleanup := setup(t, func(client *api.Client) error {
		return client.Sys().Mount(kv, &api.MountInput{
			Type: "kv",
			Options: map[string]string{
				"version": "1",
			},
		})
	})
	defer cleanup()

	kvdir := filepath.Join(dir, kv)
	if diff := cmp.Diff(readents(t, kvdir), []string{}); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}

	vwrite(t, client, filepath.Join(kv, "foo"), map[string]interface{}{
		"a": 1,
	})

	if diff := cmp.Diff(readents(t, kvdir), []string{"foo"}); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}

	b, err := ioutil.ReadFile(filepath.Join(kvdir, "foo"))
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(string(b), `{"a":1}`); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}
}

func TestKVV2(t *testing.T) {
	kv := "kvv2"
	dir, client, cleanup := setup(t, func(client *api.Client) error {
		return client.Sys().Mount(kv, &api.MountInput{
			Type: "kv",
			Options: map[string]string{
				"version": "2",
			},
		})
	})
	defer cleanup()

	kvdir := filepath.Join(dir, kv)
	if diff := cmp.Diff(readents(t, kvdir), []string{}); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}

	vwrite(t, client, filepath.Join(kv, "data/foo"), map[string]interface{}{
		"data": map[string]interface{}{
			"a": 1,
		},
	})

	if diff := cmp.Diff(readents(t, kvdir), []string{"foo"}); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}

	b, err := ioutil.ReadFile(filepath.Join(kvdir, "foo"))
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(string(b), `{"a":1}`); len(diff) > 0 {
		t.Fatalf("diff=%s", diff)
	}
}
