package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-json-experiment/json"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
)

type RootInfo struct {
	DaemonPort uint16 `json:"daemon_port,omitempty"`
}

type Info struct {
	Name         string     `json:"name,omitempty"`
	KubeContext  string     `json:"kube_context,omitempty"`
	Namespace    string     `json:"namespace,omitempty"`
	DaemonPort   uint16     `json:"daemon_port,omitempty"`
	ExposedPorts []string   `json:"exposed_ports,omitempty"`
	Hostname     string     `json:"hostname,omitempty"`
	ContainerPID int        `json:"container_pid,omitempty"`
	ContainerIP  netip.Addr `json:"container_ip,omitempty"`
	ContainerID  string     `json:"container_id,omitempty"`
	ComposeFile  string     `json:"compose_file,omitempty"`
}

type TCPInfo interface {
	RootInfo | Info
}

func (info *Info) DaemonID() *Identifier {
	return NewIdentifier(info.Name, info.KubeContext, info.Namespace, info.InDocker())
}

func (info *Info) InDocker() bool {
	return info.ContainerPID != 0
}

func (info *Info) SetConnectionInfo(name string, clusterContext string, namespace string) {
	info.Name = name
	info.KubeContext = clusterContext
	info.Namespace = namespace
}

const (
	daemonsDirName     = "userd"
	rootDaemonsDirName = "rootd"
	keepAliveInterval  = 2 * time.Second
	maxNoSignOfLife    = 3 * keepAliveInterval
)

type InfoLoader[T TCPInfo] struct {
	ctx     context.Context
	dirName string
}

func NewUserInfoLoader(ctx context.Context) *InfoLoader[Info] {
	return &InfoLoader[Info]{ctx: ctx, dirName: daemonsDirName}
}

func NewRootInfoLoader(ctx context.Context, managed bool) *InfoLoader[RootInfo] {
	if managed {
		// The root daemon is running as a service, so use the root cache dir
		ctx = filelocation.WithAppUserCacheDir(ctx, filelocation.RootCacheDir)
	}
	return &InfoLoader[RootInfo]{ctx: ctx, dirName: rootDaemonsDirName}
}

func LoadRootServiceInfo(ctx context.Context) (*RootInfo, error) {
	return NewRootInfoLoader(ctx, true).LoadInfo(InfoFileName)
}

func (il *InfoLoader[T]) LoadInfo(file string) (*T, error) {
	path := filepath.Join(filelocation.AppUserCacheDir(il.ctx), il.dirName, file)
	f, err := dos.OpenFile(dos.WithLockedFs(il.ctx), path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err == nil {
		err = il.deleteIfStale(file, fi)
	}
	if err != nil {
		return nil, err
	}
	jsonContent, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var di T
	if err := json.Unmarshal(jsonContent, &di); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from file %s: %w", path, err)
	}
	return &di, nil
}

func (il *InfoLoader[T]) SaveInfo(object *T, file string) error {
	return cache.SaveToUserCache(il.ctx, object, filepath.Join(il.dirName, file), cache.Public)
}

func (il *InfoLoader[T]) DeleteInfo(file string) error {
	return cache.DeleteFromUserCache(il.ctx, filepath.Join(il.dirName, file))
}

func (il *InfoLoader[T]) InfoExists(file string) (bool, error) {
	st, err := dos.Stat(dos.WithLockedFs(il.ctx), filepath.Join(filelocation.AppUserCacheDir(il.ctx), il.dirName, file))
	if err == nil {
		err = il.deleteIfStale(file, st)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return false, err
	}
	return true, nil
}

func (il *InfoLoader[T]) WatchInfos(onChange func(context.Context) error, files ...string) error {
	return cache.WatchUserCache(il.ctx, il.dirName, onChange, files...)
}

func (il *InfoLoader[T]) WaitUntilAllVanishes(ttw time.Duration) error {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		files, err := il.infoFiles()
		if err != nil || len(files) == 0 {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("timeout while waiting for daemon files to vanish")
}

func (il *InfoLoader[T]) DeleteAllInfos() error {
	files, err := il.infoFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		_ = cache.DeleteFromUserCache(il.ctx, filepath.Join(il.dirName, file.Name()))
	}
	return nil
}

func (il *InfoLoader[T]) LoadInfos() ([]*T, error) {
	files, err := il.infoFiles()
	if err != nil {
		return nil, err
	}

	DaemonInfos := make([]*T, len(files))
	for i, file := range files {
		if err = cache.LoadFromUserCache(il.ctx, &DaemonInfos[i], filepath.Join(il.dirName, file.Name())); err != nil {
			return nil, err
		}
	}
	return DaemonInfos, nil
}

func (il *InfoLoader[T]) DialDaemon(ctx context.Context, waitForConnect bool) (conn *grpc.ClientConn, err error) {
	var info *T
	var cancel context.CancelFunc
	if waitForConnect {
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = backoff.Retry(func() error {
			info, err = il.LoadInfo(InfoFileName)
			return err
		}, backoff.WithContext(backoff.NewConstantBackOff(200*time.Millisecond), ctx))
	} else {
		ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
		info, err = il.LoadInfo(InfoFileName)
	}
	defer cancel()
	if err != nil {
		return nil, fs.ErrNotExist
	}
	if ii, ok := any(info).(*Info); ok {
		conn, err = dialDaemon(ctx, "user", ii.DaemonPort)
	} else {
		conn, err = dialDaemon(ctx, "root", (any(info).(*RootInfo)).DaemonPort)
	}
	if errors.Is(err, context.DeadlineExceeded) && !waitForConnect {
		// A race may occur where the daemon is shutting down. We found the info file, but the daemon has since stopped responding.
		err = fs.ErrNotExist
	}
	return conn, err
}

func dialDaemon(ctx context.Context, name string, port uint16) (conn *grpc.ClientConn, err error) {
	conn, err = grpcClient.DialGRPC(ctx, fmt.Sprintf(":%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy())
	if err != nil {
		err = fmt.Errorf("unable to dial %s daemon port %d: %w", name, port, err)
	}
	return conn, err
}

func (il *InfoLoader[T]) infoFiles() ([]fs.DirEntry, error) {
	files, err := os.ReadDir(filepath.Join(filelocation.AppUserCacheDir(il.ctx), il.dirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return nil, err
	}
	active := make([]fs.DirEntry, 0, len(files))
	for _, file := range files {
		fi, err := file.Info()
		if err != nil {
			return nil, err
		}
		err = il.deleteIfStale(file.Name(), fi)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		active = append(active, file)
	}
	return active, err
}

func (il *InfoLoader[T]) deleteIfStale(name string, fi fs.FileInfo) error {
	age := time.Since(fi.ModTime())
	if age > maxNoSignOfLife {
		name = filepath.Join(il.dirName, name)
		dlog.Debugf(il.ctx, "Deleting stale info %s with age = %s", name, age)
		if err := cache.DeleteFromUserCache(il.ctx, name); err != nil {
			return err
		}
		return fs.ErrNotExist
	}
	return nil
}

func infoName[T TCPInfo](info *T) string {
	if ii, ok := any(info).(*Info); ok {
		return ii.DaemonID().String()
	}
	return "rootd"
}

type InfoMatchError string

func (i InfoMatchError) Error() string {
	return string(i)
}

type MultipleDaemonsError []*Info //nolint:errname // Don't want a plural name just because the type is a slice

func (m MultipleDaemonsError) Error() string {
	sb := strings.Builder{}
	sb.WriteString("multiple daemons are running, please select ")
	l := len(m)
	i := 0
	if l > 2 {
		sb.WriteString("one of ")
		for ; i+2 < l; i++ {
			sb.WriteString(m[i].DaemonID().Name)
			sb.WriteString(", ")
		}
	} else {
		sb.WriteString(m[i].DaemonID().Name)
		i++
	}
	sb.WriteString(" or ")
	sb.WriteString(m[i].DaemonID().Name)
	sb.WriteString(" using the --use <match> flag")
	return sb.String()
}

// LoadMatchingInfo loads the daemon info matching the given regexp or returns an error if there is none or more than one.
func (il *InfoLoader[T]) LoadMatchingInfo(match *regexp.Regexp) (*T, error) {
	infos, err := il.LoadInfos()
	if err != nil {
		return nil, err
	}
	if match != nil {
		infos = slices.DeleteFunc(infos, func(i *T) bool {
			return !match.MatchString(infoName(i))
		})
	}
	switch len(infos) {
	case 0:
		return nil, os.ErrNotExist
	case 1:
		return infos[0], nil
	default:
		if iis, ok := any(infos).([]*Info); ok {
			return nil, MultipleDaemonsError(iis)
		}
		return nil, fmt.Errorf("unexpectedly found multiple %T infos", infos[0])
	}
}

// CancelWhenRmFromCache watches for the file to be removed from the cache, then calls cancel.
func (il *InfoLoader[T]) CancelWhenRmFromCache(cancel context.CancelFunc, filename string) error {
	return il.WatchInfos(func(ctx context.Context) error {
		exists, err := il.InfoExists(filename)
		if err != nil {
			return err
		}
		if !exists {
			// spec removed from cache, shut down gracefully
			dlog.Infof(ctx, "daemon file %s removed from cache, shutting down gracefully", filename)
			cancel()
		}
		return nil
	}, filename)
}

// KeepInfoAlive updates the access and modification times of the given file
// periodically so that it never gets older than keepAliveInterval. This means that
// any file with a modification time older than the current time minus three keepAliveIntervals
// can be considered stale and should be removed.
//
// The alive-poll ends, and the file is deleted when the context is canceled.
func (il *InfoLoader[T]) KeepInfoAlive(file string) error {
	daemonFile := filepath.Join(filelocation.AppUserCacheDir(il.ctx), il.dirName, file)
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()
	now := time.Now()
	for {
		if err := os.Chtimes(daemonFile, now, now); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// File is removed, so stop trying to update its timestamps
				dlog.Debugf(il.ctx, "Daemon info %s does not exist", daemonFile)
				return nil
			}
			return fmt.Errorf("failed to update timestamp on %s: %w", daemonFile, err)
		}
		select {
		case <-il.ctx.Done():
			dlog.Debugf(il.ctx, "Deleting daemon info %s because context was cancelled", file)
			_ = il.DeleteInfo(file)
			return nil
		case now = <-ticker.C:
		}
	}
}
