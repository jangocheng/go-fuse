package fuse

// Written with a look to http://ptspts.blogspot.com/2009/11/fuse-protocol-tutorial-for-linux-26.html
import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var mountBinary string = "/bin/fusermount"

func Socketpair(network string) (l, r *os.File, err os.Error) {
	var domain int
	var typ int
	switch network {
	case "unix":
		domain = syscall.AF_UNIX
		typ = syscall.SOCK_STREAM
	case "unixgram":
		domain = syscall.AF_UNIX
		typ = syscall.SOCK_SEQPACKET
	default:
		panic("unknown network " + network)
	}
	fd, errno := syscall.Socketpair(domain, typ, 0)
	if errno != 0 {
		return nil, nil, os.NewSyscallError("socketpair", errno)
	}
	l = os.NewFile(fd[0], "socketpair-half1")
	r = os.NewFile(fd[1], "socketpair-half2")
	return
}

// Create a FUSE FS on the specified mount point.  The returned
// mount point is always absolute.
func mount(mountPoint string, options string) (f *os.File, finalMountPoint string, err os.Error) {
	local, remote, err := Socketpair("unixgram")
	if err != nil {
		return
	}

	defer local.Close()
	defer remote.Close()

	mountPoint = filepath.Clean(mountPoint)
	if !filepath.IsAbs(mountPoint) {
		cwd, err := os.Getwd()
		if err != nil {
			return
		}
		mountPoint = filepath.Clean(filepath.Join(cwd, mountPoint))
	}

	cmd := []string{mountBinary, mountPoint}
	if options != "" {
		cmd = append(cmd, "-o")
		cmd = append(cmd, options)
	}

	proc, err := os.StartProcess(mountBinary,
		cmd,
		&os.ProcAttr{
			Env:   []string{"_FUSE_COMMFD=3"},
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, remote}})

	if err != nil {
		return
	}
	w, err := os.Wait(proc.Pid, 0)
	if err != nil {
		return
	}
	if w.ExitStatus() != 0 {
		err = os.NewError(fmt.Sprintf("fusermount exited with code %d\n", w.ExitStatus()))
		return
	}

	f, err = getConnection(local)
	finalMountPoint = mountPoint
	return
}

func privilegedUnmount(mountPoint string) os.Error {
	maxTry := 2
	delay := int64(0)

	errNo := syscall.Unmount(mountPoint, 0)
	for try := 0; errNo != 0 && try < maxTry; try++ {
		// A file close operation must be processed and acked
		// by the daemon. This takes some time, so retry if
		// the first unmount fails.
		delay = 2*delay + 0.01e9
		time.Sleep(delay)
		errNo = syscall.Unmount(mountPoint, 0)
	}
	if errNo == 0 {
		return nil
	}
	return os.Errno(errNo)
}

func unmount(mountPoint string) (err os.Error) {
	dir, _ := filepath.Split(mountPoint)
	proc, err := os.StartProcess(mountBinary,
		[]string{mountBinary, "-u", mountPoint},
		&os.ProcAttr{Dir: dir, Files: []*os.File{nil, nil, os.Stderr}})
	if err != nil {
		return
	}
	w, err := os.Wait(proc.Pid, 0)
	if err != nil {
		return
	}
	if w.ExitStatus() != 0 {
		return os.NewError(fmt.Sprintf("fusermount -u exited with code %d\n", w.ExitStatus()))
	}
	return
}

func getConnection(local *os.File) (f *os.File, err os.Error) {
	var data [4]byte
	control := make([]byte, 4*256)

	// n, oobn, recvflags, from, errno  - todo: error checking.
	_, oobn, _, _,
		errno := syscall.Recvmsg(
		local.Fd(), data[:], control[:], 0)
	if errno != 0 {
		return
	}

	message := *(*syscall.Cmsghdr)(unsafe.Pointer(&control[0]))
	fd := *(*int32)(unsafe.Pointer(uintptr(unsafe.Pointer(&control[0])) + syscall.SizeofCmsghdr))

	if message.Type != 1 {
		err = os.NewError(fmt.Sprintf("getConnection: recvmsg returned wrong control type: %d", message.Type))
		return
	}
	if oobn <= syscall.SizeofCmsghdr {
		err = os.NewError(fmt.Sprintf("getConnection: too short control message. Length: %d", oobn))
		return
	}
	if fd < 0 {
		err = os.NewError(fmt.Sprintf("getConnection: fd < 0: %d", fd))
		return
	}
	f = os.NewFile(int(fd), "<fuseConnection>")
	return
}

func init() {
	for _, v := range strings.Split(os.Getenv("PATH"), ":") {
		tpath := path.Join(v, "fusermount")
		fi, err := os.Stat(tpath)
		if err == nil && (fi.Mode&0111) != 0 {
			mountBinary = tpath
			break
		}
	}
}
