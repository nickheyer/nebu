// Package sandbox runs build steps on hosts or in containers.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
)

// Mount point of the build root inside a container
const Mount = "/work"

// One command with its environment, Dir relative to the root
type Step struct {
	Name    string
	Command []string
	Env     map[string]string
	Dir     string
}

// Build step runner.
type Runner interface {
	Path(rel string) string
	Run(ctx context.Context, step Step, out io.Writer) error
}

// Runs steps directly on the host under root
type Host struct {
	root string
}

// Builds a host runner rooted at dir
func NewHost(root string) *Host { return &Host{root: root} }

// Returns the host path of a build relative path
func (h *Host) Path(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h *Host) Run(ctx context.Context, step Step, out io.Writer) error {
	if len(step.Command) == 0 {
		return fmt.Errorf("step %s: empty command", step.Name)
	}
	cmd := exec.CommandContext(ctx, step.Command[0], step.Command[1:]...)
	cmd.Dir = h.Path(step.Dir)
	cmd.Env = append(os.Environ(), envList(step.Env)...)
	cmd.Stdout, cmd.Stderr = out, out
	// Cancel the full process tree.
	return proc.Run(cmd)
}

// Runs each step in a fresh container, root mounted
type OCI struct {
	root  string
	cli   string
	image string
	args  []string
}

// Builds a container runner
func NewOCI(root, cli, image string, args []string) *OCI {
	return &OCI{root: root, cli: cli, image: image, args: args}
}

// Returns the container path of a build relative path
func (o *OCI) Path(rel string) string { return strings.TrimRight(Mount+"/"+filepath.ToSlash(rel), "/") }

func (o *OCI) Run(ctx context.Context, step Step, out io.Writer) error {
	if len(step.Command) == 0 {
		return fmt.Errorf("step %s: empty command", step.Name)
	}
	if o.image == "" {
		return fmt.Errorf("step %s: no container image configured", step.Name)
	}
	// Use the container name to stop it when the CLI is cancelled.
	name := fmt.Sprintf("nebu-build-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := []string{"run", "--rm", "--name", name, "-v", o.root + ":" + Mount, "-w", o.Path(step.Dir)}
	for _, kv := range envList(step.Env) {
		args = append(args, "-e", kv)
	}
	args = append(args, o.args...)
	args = append(args, o.image)
	args = append(args, step.Command...)
	cmd := exec.CommandContext(ctx, o.cli, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error {
		exec.Command(o.cli, "kill", name).Run()
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = proc.WaitDelay
	return cmd.Run()
}

// Returns the first container CLI found on PATH
func Detect(candidates []string) (string, bool) {
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, true
		}
	}
	return "", false
}

// Returns the tools not found on PATH
func MissingTools(tools []string) []string {
	var missing []string
	for _, t := range tools {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	return missing
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
