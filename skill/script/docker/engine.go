// Package docker runs approved Python Skill scripts in disposable containers.
// It uses the official Docker Go SDK, never the Docker CLI or a host shell.
package docker

import (
	"context"
	"errors"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"io"
	"os"
	"sync"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ErrImageNotFound is returned only for a missing local image, not connection or
// permission failures. Engine test doubles must preserve this distinction.
var ErrImageNotFound = errors.New("docker: image not found")

// ContainerSpec is the complete application-owned sandbox configuration.
type ContainerSpec struct {
	Name   string
	Config *container.Config
	Host   *container.HostConfig
}

// Engine is the narrow Docker lifecycle boundary. Logs must be multiplexed
// stdout/stderr and closing their reader must unblock reads. Wait honors context.
type Engine interface {
	Ping(context.Context) error
	ImageInspect(context.Context, string) (string, error)
	ImagePull(context.Context, string) error
	Create(context.Context, ContainerSpec) (string, error)
	Start(context.Context, string) error
	Wait(context.Context, string) (int, bool, error)
	Logs(context.Context, string) (io.ReadCloser, error)
	Remove(context.Context, string) error
	Close() error
}

type sdkEngine struct{ client *client.Client }

func newEngine() (Engine, error) {
	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &sdkEngine{c}, nil
}
func (e *sdkEngine) Ping(ctx context.Context) error {
	_, err := e.client.Ping(ctx, client.PingOptions{})
	return classifyEngineError(err)
}
func (e *sdkEngine) ImageInspect(ctx context.Context, image string) (string, error) {
	result, err := e.client.ImageInspect(ctx, image)
	if errdefs.IsNotFound(err) {
		return "", ErrImageNotFound
	}
	return result.ID, classifyEngineError(err)
}

func classifyEngineError(err error) error {
	if errdefs.IsPermissionDenied(err) || errdefs.IsUnauthorized(err) || errors.Is(err, os.ErrPermission) {
		return script.Failure(script.CodePermission, err)
	}
	return err
}
func (e *sdkEngine) ImagePull(ctx context.Context, image string) error {
	response, err := e.client.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer response.Close()
	return response.Wait(ctx)
}
func (e *sdkEngine) Create(ctx context.Context, s ContainerSpec) (string, error) {
	result, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{Name: s.Name, Config: s.Config, HostConfig: s.Host})
	return result.ID, err
}
func (e *sdkEngine) Start(ctx context.Context, id string) error {
	_, err := e.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}
func (e *sdkEngine) Wait(ctx context.Context, id string) (int, bool, error) {
	waiting := e.client.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	// Receive the SDK's final result even on cancellation, so its unbuffered
	// result producer cannot be stranded by an early context branch.
	select {
	case result := <-waiting.Result:
		if result.Error != nil {
			return int(result.StatusCode), false, errors.New("container wait failed")
		}
		inspected, err := e.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return int(result.StatusCode), false, err
		}
		oom := inspected.Container.State != nil && inspected.Container.State.OOMKilled
		return int(result.StatusCode), oom, nil
	case err := <-waiting.Error:
		return -1, false, err
	}
}
func (e *sdkEngine) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	// Attach before Start so a fast script cannot rotate/drop early output
	// before the bounded collector subscribes. No daemon log files are needed.
	response, err := e.client.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return nil, err
	}
	return &attachedLogs{response: response}, nil
}

type attachedLogs struct {
	response client.ContainerAttachResult
	once     sync.Once
}

func (r *attachedLogs) Read(b []byte) (int, error) { return r.response.Reader.Read(b) }
func (r *attachedLogs) Close() error               { r.once.Do(func() { r.response.Close() }); return nil }
func (e *sdkEngine) Remove(ctx context.Context, id string) error {
	_, err := e.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}
func (e *sdkEngine) Close() error { return e.client.Close() }
