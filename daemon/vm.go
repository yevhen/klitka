package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type VM struct {
	ID     string
	Root   string
	Mounts []Mount
}

type Mount struct {
	GuestPath string
	HostPath  string
	Mode      klitkavmv1.MountMode
}

func newVM(id string, req *klitkavmv1.StartVMRequest) (*VM, error) {
	root, err := os.MkdirTemp("", fmt.Sprintf("klitkavm-%s-", id))
	if err != nil {
		return nil, err
	}
	if err := prepareRoot(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	mounts, err := buildMounts(root, req.GetMounts())
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &VM{ID: id, Root: root, Mounts: mounts}, nil
}

func (vm *VM) Cleanup() {
	if vm == nil {
		return
	}
	if vm.Root != "" {
		_ = os.RemoveAll(vm.Root)
	}
}

func (vm *VM) RewriteCommand(command string, args []string) (string, []string) {
	command = vm.rewritePath(command, false)
	if len(args) == 0 {
		return command, args
	}

	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = vm.rewritePath(arg, true)
	}
	return command, out
}

func (vm *VM) rewritePath(input string, allowRoot bool) string {
	if !strings.HasPrefix(input, string(os.PathSeparator)) {
		return input
	}

	for _, mount := range vm.Mounts {
		guest := mount.GuestPath
		if input == guest {
			return mount.HostPath
		}
		if strings.HasPrefix(input, guest+string(os.PathSeparator)) {
			suffix := strings.TrimPrefix(input, guest)
			return filepath.Clean(mount.HostPath + suffix)
		}
	}

	if !allowRoot || vm.Root == "" {
		return input
	}

	return vm.rootPath(input)
}

func prepareRoot(root string) error {
	return os.MkdirAll(filepath.Join(root, "tmp"), 0o755)
}

func (vm *VM) rootPath(input string) string {
	if input == string(os.PathSeparator) {
		return vm.Root
	}
	trimmed := strings.TrimPrefix(input, string(os.PathSeparator))
	target := filepath.Join(vm.Root, trimmed)
	parent := filepath.Dir(target)
	if parent != "" && parent != vm.Root {
		_ = os.MkdirAll(parent, 0o755)
	}
	return target
}

func buildMounts(root string, mounts []*klitkavmv1.Mount) ([]Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}

	out := make([]Mount, 0, len(mounts))
	for _, mount := range mounts {
		guestPath := filepath.Clean(mount.GetGuestPath())
		if guestPath == "." || guestPath == "" || !filepath.IsAbs(guestPath) {
			return nil, fmt.Errorf("invalid guest path: %q", mount.GetGuestPath())
		}

		hostPath := filepath.Clean(mount.GetHostPath())
		if hostPath == "." || hostPath == "" {
			return nil, fmt.Errorf("invalid host path: %q", mount.GetHostPath())
		}

		if _, err := os.Stat(hostPath); err != nil {
			return nil, fmt.Errorf("host path not found: %s", hostPath)
		}

		mode := mount.GetMode()
		if mode == klitkavmv1.MountMode_MOUNT_MODE_UNSPECIFIED {
			mode = klitkavmv1.MountMode_MOUNT_MODE_RO
		}

		resolvedHostPath := hostPath
		if mode == klitkavmv1.MountMode_MOUNT_MODE_RO {
			copyPath := filepath.Join(root, "mounts", strings.TrimPrefix(guestPath, string(os.PathSeparator)))
			if err := copyTree(hostPath, copyPath); err != nil {
				return nil, err
			}
			if err := makeReadOnly(copyPath); err != nil {
				return nil, err
			}
			resolvedHostPath = copyPath
		}

		out = append(out, Mount{
			GuestPath: guestPath,
			HostPath:  resolvedHostPath,
			Mode:      mode,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].GuestPath) > len(out[j].GuestPath)
	})

	return out, nil
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}

	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode()&os.ModePerm)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return os.Chmod(dst, mode&os.ModePerm)
}

func makeReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o555)
		}
		mode := info.Mode() & os.ModePerm
		if mode == 0 {
			mode = 0o444
		}
		return os.Chmod(path, 0o444)
	})
}
