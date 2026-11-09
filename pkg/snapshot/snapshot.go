// Package snapshot dumps a ready runtime process with CRIU, its CUDA state parked by cuda-checkpoint, and restores it later
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Host facts the checkpoint probe records the tool paths under
const (
	FactCRIU           = "criu"
	FactCRIUVersion    = "criu_version"
	FactCUDACheckpoint = "cuda_checkpoint"
)

// Names of the files an image directory holds beside the CRIU images
const (
	metaFile = "nebu-snapshot.json"
	logFile  = "criu.log"
	pidFile  = "restored.pid"
	imageDir = "images"
)

// Longest a cuda-checkpoint lock waits for running kernels before it fails
const cudaLockTimeout = 2 * time.Minute

// Returned when the host probe found no tool the snapshot needs
var ErrMissingTool = errors.New("snapshot tool missing")

// Paths of the checkpoint tools the host probe found
type Tools struct {
	CRIU           string
	CUDACheckpoint string
}

// The tool paths a probed profile records, refusing a profile without criu or, with nvidia set, without cuda-checkpoint
func Detect(profile *v1.HostProfile, nvidia bool) (Tools, error) {
	facts := profile.GetFacts()
	t := Tools{CRIU: facts[FactCRIU], CUDACheckpoint: facts[FactCUDACheckpoint]}
	if t.CRIU == "" {
		return Tools{}, fmt.Errorf("%w: the host probe found no criu on PATH, install criu 4.0 or newer to snapshot processes", ErrMissingTool)
	}
	if nvidia && t.CUDACheckpoint == "" {
		return Tools{}, fmt.Errorf("%w: the host probe found no cuda-checkpoint on PATH, install it from NVIDIA/cuda-checkpoint (driver 550 or newer) to snapshot a process holding a GPU", ErrMissingTool)
	}
	return t, nil
}

// What a dumped image restores: the root pid, the port it listens on, the pids holding CUDA state, the log it wrote, and what it ran
type Meta struct {
	Pid     int    `json:"pid"`
	Port    int    `json:"port"`
	CUDA    []int  `json:"cuda_pids"`
	LogPath string `json:"log_path"`
	TakenAt string `json:"taken_at"`
	// The command as recorded on the instance, its params, and what the runtime reported it serves
	Command   []string             `json:"command,omitempty"`
	Params    map[string]string    `json:"params,omitempty"`
	Inventory *v1.RuntimeInventory `json:"inventory,omitempty"`
}

// Dumps and restores process trees through the tools
type Snapshotter struct {
	Tools Tools
	Log   *slog.Logger
	// Reads the pids of a tree holding an NVIDIA device, the kernel's fd table unless a test names them another way
	CUDAPIDs func(root int) ([]int, error)
}

// What to dump
type Request struct {
	// The root of the process tree
	Pid int
	// The directory the image lands in, replaced whole
	Dir string
	// The port the root listens on, restored as is
	Port int
	// The file the process writes its output to, inherited anew on restore
	LogPath string
	// The command as recorded, its params, and what the runtime reported it serves, carried onto the restored record
	Command   []string
	Params    map[string]string
	Inventory *v1.RuntimeInventory
}

// Parks the tree's CUDA state, dumps the tree leaving it running, and brings the CUDA state back
func (s *Snapshotter) Dump(ctx context.Context, req Request) (*Meta, error) {
	if err := os.RemoveAll(req.Dir); err != nil {
		return nil, err
	}
	images := filepath.Join(req.Dir, imageDir)
	if err := os.MkdirAll(images, 0o700); err != nil {
		return nil, err
	}
	cuda, err := s.holders(req.Pid)
	if err != nil {
		os.RemoveAll(req.Dir)
		return nil, err
	}
	if len(cuda) > 0 && s.Tools.CUDACheckpoint == "" {
		os.RemoveAll(req.Dir)
		return nil, fmt.Errorf("%w: pids %v hold CUDA state and no cuda-checkpoint was found", ErrMissingTool, cuda)
	}
	var parked []int
	for _, pid := range cuda {
		if err := s.cuda(ctx, "lock", pid, "--timeout", strconv.FormatInt(cudaLockTimeout.Milliseconds(), 10)); err != nil {
			os.RemoveAll(req.Dir)
			return nil, errors.Join(err, s.resume(ctx, parked))
		}
		if err := s.cuda(ctx, "checkpoint", pid); err != nil {
			os.RemoveAll(req.Dir)
			unlock := s.cuda(ctx, "unlock", pid)
			return nil, errors.Join(err, unlock, s.resume(ctx, parked))
		}
		parked = append(parked, pid)
	}
	dumpErr := s.criu(ctx, req.Dir, "dump", "--tree", strconv.Itoa(req.Pid), "--images-dir", images, "--leave-running", "--tcp-close", "--shell-job")
	if err := errors.Join(dumpErr, s.resume(ctx, parked)); err != nil {
		os.RemoveAll(req.Dir)
		return nil, err
	}
	meta := &Meta{Pid: req.Pid, Port: req.Port, CUDA: cuda, LogPath: req.LogPath, TakenAt: time.Now().UTC().Format(time.RFC3339), Command: req.Command, Params: req.Params, Inventory: req.Inventory}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(req.Dir, metaFile), data, 0o600); err != nil {
		return nil, err
	}
	return meta, nil
}

// Brings the CUDA state of every parked pid back and unlocks it, every failure joined
func (s *Snapshotter) resume(ctx context.Context, parked []int) error {
	var errs []error
	for _, pid := range parked {
		if err := s.cuda(ctx, "restore", pid); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.cuda(ctx, "unlock", pid); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Pids of a tree holding an NVIDIA device, through the reader a test installs or the kernel's fd table
func (s *Snapshotter) holders(root int) ([]int, error) {
	if s.CUDAPIDs != nil {
		return s.CUDAPIDs(root)
	}
	return cudaPIDs(root)
}

// The meta of an image directory, false when the directory holds no finished image
func Read(dir string) (*Meta, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, false, fmt.Errorf("%s: %w", filepath.Join(dir, metaFile), err)
	}
	return &meta, true, nil
}

// Restores the tree as a child of this process writing to logPath, brings its CUDA state back, and returns the root pid
func (s *Snapshotter) Restore(ctx context.Context, dir, logPath string) (int, error) {
	meta, ok, err := Read(dir)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("%s holds no snapshot", dir)
	}
	if len(meta.CUDA) > 0 && s.Tools.CUDACheckpoint == "" {
		return 0, fmt.Errorf("%w: the image parks CUDA state of pids %v and no cuda-checkpoint was found", ErrMissingTool, meta.CUDA)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	pidPath := filepath.Join(dir, pidFile)
	os.Remove(pidPath)
	inherit := fmt.Sprintf("fd[3]:%s", strings.TrimPrefix(meta.LogPath, "/"))
	cmd := s.command(ctx, dir, "restore", "--images-dir", filepath.Join(dir, imageDir), "--restore-detached", "--restore-sibling", "--pidfile", pidPath, "--tcp-close", "--shell-job", "--inherit-fd", inherit)
	cmd.ExtraFiles = []*os.File{out}
	if err := s.run(cmd); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, fmt.Errorf("criu wrote no pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("criu pidfile: %w", err)
	}
	var errs []error
	for _, cpid := range meta.CUDA {
		if err := s.cuda(ctx, "restore", cpid); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.cuda(ctx, "unlock", cpid); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return 0, errors.Join(err, discard(pid))
	}
	return pid, nil
}

// Runs one criu subcommand with its log in the image directory, the log's tail on failure
func (s *Snapshotter) criu(ctx context.Context, dir, verb string, args ...string) error {
	return s.run(s.command(ctx, dir, verb, args...))
}

// A criu command in its own process group, where a shell job restore lands the tree, logging into the image directory
func (s *Snapshotter) command(ctx context.Context, dir, verb string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.Tools.CRIU, append([]string{verb, "--log-file", filepath.Join(dir, logFile)}, args...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = proc.Attr()
	return cmd
}

// Runs one cuda-checkpoint action on a pid
func (s *Snapshotter) cuda(ctx context.Context, action string, pid int, extra ...string) error {
	args := append([]string{"--action", action, "--pid", strconv.Itoa(pid)}, extra...)
	if err := s.run(exec.CommandContext(ctx, s.Tools.CUDACheckpoint, args...)); err != nil {
		return fmt.Errorf("cuda-checkpoint %s of pid %d: %w", action, pid, err)
	}
	return nil
}

// Runs a tool, logging what ran and how long it took, its combined output in the error when it fails
func (s *Snapshotter) run(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	s.Log.Info("snapshot tool ran", "tool", filepath.Base(cmd.Path), "args", strings.Join(cmd.Args[1:], " "), "took", time.Since(start).Round(time.Millisecond), "ok", err == nil)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out.String())
	if logs := criuLogTail(cmd); logs != "" {
		text = strings.TrimSpace(text + "\n" + logs)
	}
	return fmt.Errorf("%s %s: %w: %s", filepath.Base(cmd.Path), strings.Join(cmd.Args[1:min(len(cmd.Args), 2)], " "), err, text)
}

// The last lines of the criu log a command named, empty for a command without one
func criuLogTail(cmd *exec.Cmd) string {
	for i, a := range cmd.Args {
		if a == "--log-file" && i+1 < len(cmd.Args) {
			data, err := os.ReadFile(cmd.Args[i+1])
			if err != nil {
				return ""
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			return strings.Join(lines, "\n")
		}
	}
	return ""
}
