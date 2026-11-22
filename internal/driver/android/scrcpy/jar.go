package scrcpy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// The server jar is pushed to a phone once per Pinky version, to a path
// named by its content, and each run gets its own hard link to it.
//
// A run needs a name of its own because the server deletes the jar it runs
// from: scrcpy's CleanUp.unlinkSelf removes the first CLASSPATH entry once
// the server starts (and in list mode, before it lists). A hard link loses
// only that name, so the shared copy stays for the next run, and runs that
// start at once (dark.go's helpers) cannot delete each other's jar.
//
// Pushing a 734 KB jar for every run took the 20 s for opening a
// background display on a slow tailnet link, where opening one starts
// three runs.

const (
	jarDir    = "/data/local/tmp"
	jarPrefix = "pinky-scrcpy-"
)

// jarPushTimeout bounds checking and pushing the shared jar. The push runs
// on its own, apart from the start that asked for it: one that gives up
// leaves it going, and the next start finds it done.
var jarPushTimeout = 3 * time.Minute

// jarTag names this jar: the server's version and its content's hash.
var jarTag = func() string {
	sum := sha256.Sum256(serverJar)
	return Version + "-" + hex.EncodeToString(sum[:6])
}()

// sharedJarPath is where the phone keeps the jar between runs.
func sharedJarPath() string { return jarDir + "/" + jarPrefix + jarTag + ".jar" }

// runJarPath is the name a run starts the server from.
func runJarPath(scid uint32) string {
	return fmt.Sprintf("%s/%s%s-%08x.jar", jarDir, jarPrefix, jarTag, scid)
}

type jarPush struct {
	done chan struct{}
	err  error
}

// jarPushes holds the check-and-push running for each phone, so the runs
// that start at once share one.
var jarPushes struct {
	mu sync.Mutex
	m  map[string]*jarPush
}

// runJar puts the server on the phone for one run and returns the path to
// start it from, which the run removes when it ends.
func runJar(ctx context.Context, c *adb.Client, serial string, scid uint32) (string, error) {
	jar := runJarPath(scid)
	err := ensureJar(ctx, c, serial)
	if err == nil {
		shared := sharedJarPath()
		// cp covers a file system that refuses hard links.
		out, lerr := c.Shell(ctx, serial, "ln -f "+shared+" "+jar+" 2>/dev/null || cp "+shared+" "+jar+" && echo ok")
		if lerr == nil && strings.TrimSpace(string(out)) == "ok" {
			return jar, nil
		}
		err = lerr
		if err == nil {
			err = fmt.Errorf("link %s: %s", jar, strings.TrimSpace(string(out)))
		}
	}
	if ctx.Err() != nil {
		return "", err
	}
	// The shared jar cannot be used: push this run's own, as before.
	if err := c.Push(ctx, serial, bytes.NewReader(serverJar), jar, 0o644, time.Now()); err != nil {
		return "", err
	}
	return jar, nil
}

// ensureJar makes sure the phone has this jar at sharedJarPath, pushing it
// when it is missing or has another size. Calls for one phone share one
// check and push, which goes on when ctx ends (see jarPushTimeout).
func ensureJar(ctx context.Context, c *adb.Client, serial string) error {
	key := c.Addr + "\x00" + serial
	jarPushes.mu.Lock()
	p := jarPushes.m[key]
	if p == nil {
		p = &jarPush{done: make(chan struct{})}
		if jarPushes.m == nil {
			jarPushes.m = map[string]*jarPush{}
		}
		jarPushes.m[key] = p
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jarPushTimeout)
		go func() {
			defer cancel()
			p.err = installJar(pctx, c, serial)
			jarPushes.mu.Lock()
			delete(jarPushes.m, key)
			jarPushes.mu.Unlock()
			close(p.done)
		}()
	}
	jarPushes.mu.Unlock()
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return fmt.Errorf("%w (the push goes on in the background)", ctx.Err())
	}
}

// installJar pushes the jar to sharedJarPath unless a file of its size is
// there. It pushes to a temporary name and renames that into place, so a
// push cut short never looks complete.
func installJar(ctx context.Context, c *adb.Client, serial string) error {
	shared := sharedJarPath()
	fi, err := c.Stat(ctx, serial, shared)
	if err != nil {
		return err
	}
	if fi.Exists() && fi.Mode.IsRegular() && fi.Size == int64(len(serverJar)) {
		return nil
	}
	removeStaleJars(ctx, c, serial)
	scid, err := randomSCID()
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%08x.tmp", shared, scid)
	if err := c.Push(ctx, serial, bytes.NewReader(serverJar), tmp, 0o644, time.Now()); err != nil {
		removeFile(c, serial, tmp)
		return err
	}
	out, err := c.Shell(ctx, serial, "mv "+tmp+" "+shared+" && echo ok")
	if err == nil && strings.TrimSpace(string(out)) != "ok" {
		err = fmt.Errorf("move %s into place: %s", tmp, strings.TrimSpace(string(out)))
	}
	if err != nil {
		removeFile(c, serial, tmp)
	}
	return err
}

// removeStaleJars deletes the jars of other versions, shared or left by
// runs that never got to delete theirs, and the random names Pinky used
// before it shared the jar. It runs before a push, so about once per
// phone and Pinky version. This version's files stay: a run may be
// starting from one.
func removeStaleJars(ctx context.Context, c *adb.Client, serial string) {
	_, _ = c.Shell(ctx, serial, staleJarsCommand())
}

func staleJarsCommand() string {
	return "cd " + jarDir + " && for f in " + jarPrefix + "*; do case \"$f\" in " +
		jarPrefix + jarTag + "*) ;; *) rm -f \"$f\" ;; esac; done"
}

// removeFile deletes path on the phone, best effort.
func removeFile(c *adb.Client, serial, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.Shell(ctx, serial, "rm -f "+path)
}
