package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/docker/go-plugins-helpers/volume"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func main() {
	// logrus.SetLevel(logrus.DebugLevel)
	logPath := "/var/log/securebind-docker.log"
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0666)
	if err == nil {
		defer log.Close()
		logrus.SetOutput(log)
	} else {
		logrus.Warnf("failed to set logrus output to %s: %v", logPath, err)
	}
	logrus.Debug("starting")
	if err := xmain(); err != nil {
		logrus.Fatal(err)
	}
}

func xmain() error {
	d, err := NewDriver("/mnt/root", "/mnt/volumes", "/var/lib/securebind-dockerjson")
	if err != nil {
		return err
	}
	d = &DriverWithLog{Driver: d}
	h := volume.NewHandler(d)
	gid := 0
	return h.ServeUnix("driver", gid)
}

const (
	OptSource = "source"
)

type State struct {
	Volumes map[string]volume.Volume
	Sources map[string]string
}

type Driver struct {
	mntRoot    string
	mntVolumes string
	mu         sync.Mutex
	state      State
	statePath  string
	fuses      map[string]FuseServer
}

func NewDriver(mntRoot, mntVolumes, statePath string) (volume.Driver, error) {
	if err := os.MkdirAll(mntRoot, 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(mntVolumes, 0755); err != nil {
		return nil, err
	}
	state := State{
		Volumes: make(map[string]volume.Volume),
		Sources: make(map[string]string),
	}
	stateJSON, err := ioutil.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(stateJSON, &state); err != nil {
			return nil, err
		}
	}
	d := &Driver{
		mntRoot:    mntRoot,
		mntVolumes: mntVolumes,
		state:      state,
		statePath:  statePath,
		fuses:      make(map[string]FuseServer),
	}
	return d, nil
}

// saveState requires d.mu to be locked
func (d *Driver) saveState() error {
	b, err := json.Marshal(d.state)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(d.statePath, b, 0644)
}

func (d *Driver) Create(req *volume.CreateRequest) error {
	if strings.Contains(req.Name, "/") {
		return errors.Errorf("securebind requires volume name not to contain '/': %q", req.Name)
	}
	src, ok := req.Options[OptSource]
	if !ok {
		return errors.Errorf("securebind requires option %q to be provided", OptSource)
	}
	if !strings.HasPrefix(src, "/") {
		return errors.Errorf("securebind requires option %q to be an absolute path, got %q", OptSource, src)
	}
	mntPoint, err := securejoin.SecureJoin(d.mntVolumes, req.Name)
	if err != nil {
		return err
	}
	v := volume.Volume{
		Name:       req.Name,
		Mountpoint: mntPoint,
		CreatedAt:  time.Now().Format("2006-01-02T15:04:05Z07:00"),
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok = d.state.Volumes[req.Name]
	if ok {
		return errors.Errorf("volume %q exists", req.Name)
	}
	if err := os.MkdirAll(mntPoint, 0755); err != nil {
		return err
	}
	d.state.Volumes[req.Name] = v
	d.state.Sources[req.Name] = src
	if err := d.saveState(); err != nil {
		return err
	}
	return nil
}

func (d *Driver) Get(req *volume.GetRequest) (*volume.GetResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.state.Volumes[req.Name]
	if !ok {
		return nil, errors.Errorf("no such volume: %q", req.Name)
	}
	return &volume.GetResponse{
		Volume: &v,
	}, nil
}

func (d *Driver) List() (*volume.ListResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var xres volume.ListResponse
	for _, v := range d.state.Volumes {
		xres.Volumes = append(xres.Volumes, &v)
	}
	return &xres, nil

}

func (d *Driver) Remove(req *volume.RemoveRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.state.Volumes[req.Name]
	if !ok {
		return errors.Errorf("no such volume: %q", req.Name)
	}
	_, ok = d.fuses[req.Name]
	if ok {
		return errors.Errorf("volume %q is in use", req.Name)
	}
	delete(d.state.Volumes, req.Name)
	delete(d.state.Sources, req.Name)
	if err := d.saveState(); err != nil {
		return err
	}
	return nil
}

func (d *Driver) Path(req *volume.PathRequest) (*volume.PathResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.state.Volumes[req.Name]
	if !ok {
		return nil, errors.Errorf("no such volume: %q", req.Name)
	}
	return &volume.PathResponse{
		Mountpoint: v.Mountpoint,
	}, nil
}

func (d *Driver) Mount(req *volume.MountRequest) (*volume.MountResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	src, ok := d.state.Sources[req.Name]
	if !ok {
		return nil, errors.Errorf("no such volume: %q", req.Name)
	}
	v, ok := d.state.Volumes[req.Name]
	if !ok {
		return nil, errors.Errorf("no such volume: %q", req.Name)
	}
	absSrc, err := securejoin.SecureJoin(d.mntRoot, src)
	if err != nil {
		return nil, err
	}
	f, err := NewFuseServer(absSrc, v.Mountpoint)
	if err != nil {
		return nil, err
	}
	go f.Serve()
	if err := f.WaitMount(); err != nil {
		return nil, err
	}
	d.fuses[req.Name] = f
	return &volume.MountResponse{
		Mountpoint: v.Mountpoint,
	}, nil
}

func (d *Driver) Unmount(req *volume.UnmountRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.fuses[req.Name]
	if !ok {
		return errors.Errorf("volume %q is not mounted", req.Name)
	}
	if err := f.Unmount(); err != nil {
		return err
	}
	delete(d.fuses, req.Name)
	// TODO: rmdir mountpoint
	return nil
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
}

// FuseServer is implemented by github.com/hanwen/go-fuse/fuse.Server
type FuseServer interface {
	Serve()
	WaitMount() error
	Unmount() error
}

func NewFuseServer(src, mountpoint string) (FuseServer, error) {
	loopbackfs := pathfs.NewLoopbackFileSystem(src)
	pathFs := pathfs.NewPathNodeFs(loopbackfs, nil)
	conn := nodefs.NewFileSystemConnector(pathFs.Root(), nil)
	mOpts := &fuse.MountOptions{
		Options: []string{"ro"},
	}
	return fuse.NewServer(conn.RawFS(), mountpoint, mOpts)
}

// DriverWithLog wraps volume.Driver with logrus.Debug
type DriverWithLog struct {
	volume.Driver
}

func (d *DriverWithLog) Create(req *volume.CreateRequest) error {
	logrus.Debugf("> Create %+v", req)
	err := d.Driver.Create(req)
	logrus.Debugf("< Create %+v", err)
	return err
}

func (d *DriverWithLog) Get(req *volume.GetRequest) (*volume.GetResponse, error) {
	logrus.Debugf("> Get %+v", req)
	res, err := d.Driver.Get(req)
	logrus.Debugf("< Get %+v (%+v), %v", res, res.Volume, err)
	return res, err
}

func (d *DriverWithLog) List() (*volume.ListResponse, error) {
	logrus.Debug("> List")
	res, err := d.Driver.List()
	var ss []string
	for _, v := range res.Volumes {
		ss = append(ss, fmt.Sprintf("+%v", *v))
	}
	logrus.Debugf("< List %+v ([%s]), %v", res, strings.Join(ss, ","), err)
	return res, err
}

func (d *DriverWithLog) Remove(req *volume.RemoveRequest) error {
	logrus.Debugf("> Remove %+v", req)
	err := d.Driver.Remove(req)
	logrus.Debugf("< Remove %v", err)
	return err
}

func (d *DriverWithLog) Path(req *volume.PathRequest) (*volume.PathResponse, error) {
	logrus.Debugf("> Path %+v", req)
	res, err := d.Driver.Path(req)
	logrus.Debugf("< Path %+v, %v", res, err)
	return res, err
}

func (d *DriverWithLog) Mount(req *volume.MountRequest) (*volume.MountResponse, error) {
	logrus.Debugf("> Mount %+v", req)
	res, err := d.Driver.Mount(req)
	logrus.Debugf("< Mount %+v, %v", res, err)
	return res, err
}

func (d *DriverWithLog) Unmount(req *volume.UnmountRequest) error {
	logrus.Debugf("> Unmount %+v", req)
	err := d.Driver.Unmount(req)
	logrus.Debugf("< Unmount %v", err)
	return err
}

func (d *DriverWithLog) Capabilities() *volume.CapabilitiesResponse {
	logrus.Debug("> Capabilities")
	res := d.Driver.Capabilities()
	logrus.Debugf("< Capabilities %v", res)
	return res
}
