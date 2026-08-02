//go:build linux

package server

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// netdiskACLFixed remembers, for the lifetime of this process, which netdisk
// roots have already had fixNetdiskACL's one-time recursive repair applied.
// ensureUserNetdisk calls fixNetdiskACL on every list/upload/rename/... and
// every container creation, so without this the recursive setfacl below
// would re-walk the whole netdisk tree on every single request. One pass is
// enough: it sets a default ACL on the root, and from then on the kernel
// itself propagates it to anything created under it, no re-walk needed.
var netdiskACLFixed sync.Map

// fixNetdiskACL grants the mudp process's own uid rwx access to dir via a
// POSIX ACL, both as a regular entry (to repair files/folders a container
// already left root-owned) and as a default entry (so anything created
// under dir afterwards, at any depth, inherits it automatically). Containers
// commonly run as root over the bind-mounted netdisk; once root creates
// something under it, the mudp service — which normally runs as an
// unprivileged systemd user, see scripts/install-service.sh — loses write
// access, and uploads/deletes/renames through the panel start failing with
// EACCES.
func fixNetdiskACL(dir string) {
	uid := os.Getuid()
	if uid == 0 {
		// mudp itself runs as root: nothing can deny it access.
		return
	}
	if _, already := netdiskACLFixed.Load(dir); already {
		return
	}
	spec := fmt.Sprintf("u:%d:rwx,d:u:%d:rwx", uid, uid)
	cmd := exec.Command("setfacl", "-R", "-m", spec, dir) // #nosec G204 -- dir is a server-resolved netdisk path, uid is our own process uid, no user input reaches argv
	if out, err := cmd.CombinedOutput(); err != nil {
		// Do not cache dir as fixed: a transient failure here (missing
		// setfacl, ACLs unsupported on this mount, ...) must not
		// permanently disable the repair for the process lifetime -- the
		// next call to fixNetdiskACL should retry.
		log.Printf("netdisk acl: setfacl on %s failed (netdisk uploads may break once a container writes to it as root): %v: %s", dir, err, strings.TrimSpace(string(out)))
		return
	}
	netdiskACLFixed.Store(dir, true)
}
