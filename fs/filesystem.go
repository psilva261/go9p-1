package fs

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/knusbaum/go9p/proto"
)

// FSNode represents a node in a FS tree. It should track its
// Parent, which should be the Dir that the FSNode belongs to.
// SetParent should not be called directly. Instead, use the
// AddChild and DeleteChild functions on a Dir to add, remove,
// and move FSNodes around a filesystem.
type FSNode interface {
	Stat() proto.Stat
	WriteStat(s *proto.Stat) error
	SetParent(d Dir)
	Parent() Dir
}

// File represents a leaf node in the FS tree. It must implement
// Open, Read, Write, and Close methods. fid is a unique number
// representing an open file descriptor to the file.
// For each fid, Open will be called before Read, Write, or Close,
// and should open a file in the given omode, or return an error.
// If Open returns an error, fid is not valid. Following a
// successful Open, a given fid represents the same file descriptor
// until Close is called. Read requests count bytes at offset in the
// file, for file descriptor fid. Similarly, Write should write data
// into the file from file descriptor fid at offset.
// Both Read and Write may return error, which will be sent back to
// the client in a proto.TError message.
//
// See StaticFile for a simple File implementation.
type File interface {
	FSNode
	Open(fid uint32, omode proto.Mode) error
	Read(fid uint32, offset uint64, count uint64) ([]byte, error)
	Write(fid uint32, offset uint64, data []byte) (uint32, error)
	Close(fid uint32) error
}

// Dir represents a directory within the Filesystem.
type Dir interface {
	FSNode
	Children() map[string]FSNode
	AddChild(n FSNode) error
	DeleteChild(name string) error
}

// FullPath is a helper function that assembles the names
// of all the parent nodes of f into a full path string.
func FullPath(f FSNode) string {
	if f == nil {
		return ""
	}
	parent := f.Parent()
	if parent == nil {
		fmt.Printf("ROOT: %s\n", f.Stat().Name)
		return f.Stat().Name
	}
	fp := FullPath(parent)
	fmt.Printf("Not root: %s / %s\n", fp, f.Stat().Name)
	return strings.Replace(fp+"/"+f.Stat().Name, "//", "/", -1)
}

// BaseFile provides a simple File implementation that
// other implementations can base themselves off of.
// On its own it's not too useful. Stat and WriteStat
// work as expected, as do Parent and SetParent.
// Open always succeeds. Read always returns a zero-byte
// slice, Write always fails, and Close always succeeds.
type BaseFile struct {
	fStat  proto.Stat
	parent Dir
	sync.RWMutex
}

func NewBaseFile(s *proto.Stat) *BaseFile {
	return &BaseFile{fStat: *s}
}

func (f *BaseFile) Stat() proto.Stat {
	return f.fStat
}

func (f *BaseFile) WriteStat(s *proto.Stat) error {
	f.Lock()
	defer f.Unlock()
	f.fStat = *s
	return nil
}

func (f *BaseFile) SetParent(p Dir) {
	f.Lock()
	defer f.Unlock()
	f.parent = p
}

func (f *BaseFile) Parent() Dir {
	f.RLock()
	defer f.RUnlock()
	return f.parent
}

// Open always succeeds.
func (f *BaseFile) Open(fid uint32, omode proto.Mode) error {
	return nil
}

// Read always returns an empty slice.
func (f *BaseFile) Read(fid uint32, offset uint64, count uint64) ([]byte, error) {
	return []byte{}, nil
}

// Write always fails with an error.
func (f *BaseFile) Write(fid uint32, offset uint64, data []byte) (uint32, error) {
	return 0, fmt.Errorf("Cannot write to file.")
}

// Close always succeeds.
func (f *BaseFile) Close(fid uint32) error {
	return nil
}

// The FS structure represents a hierarchical filesystem tree.
// It must contain a Root Dir, but all of the function members are
// optional. If provided, CreateFile is called when a client attempts
// to create a file. Similarly, CreateDir is called when a client attempts
// to create a directory. WalkFail's usefulness is dubious, but is called
// when a client walks to a path that does not exist in the fs. It can be used
// to create files on the fly. CreateFile returns a File, CreateDir returns a
// Dir, and WalkFail should return either a File or a Dir. All three can return
// an error, in which case the Error() will be returned to the client in a
// proto.TError. nil, nil is also an appropriate return pair, in which case a
// proto.TError with a generic message will be returned to the client.
//
// FS is a tree structure. Every internal node should be a Dir - only
// instances of Dir can have children. Instances of File can only be leaves of
// the tree.
type FS struct {
	Root       Dir
	CreateFile func(fs *FS, parent Dir, user, name string, perm uint32, mode uint8) (File, error)
	CreateDir  func(fs *FS, parent Dir, user, name string, perm uint32, mode uint8) (Dir, error)
	WalkFail   func(fs *FS, parent Dir, name string) (FSNode, error)
	RemoveFile func(fs *FS, f FSNode) error
	uid        uint64
	sync.RWMutex
}

// NewFS constructs and returns an *FS. Options may be passed to do things
// like setting the various hook functions.
func NewFS(rootUser, rootGroup string, rootPerms uint32, opts ...Option) *FS {
	var fs FS
	d := NewStaticDir(fs.NewStat("/", rootUser, rootGroup, rootPerms|proto.DMDIR))
	fs.Root = d
	for _, o := range opts {
		o(&fs)
	}
	return &fs
}

// NewQid generates a new, unique proto.Qid for use in a new file.
// Each file in the FS should have a unique proto.Qid. statMode
// should come from the file's Stat().Mode
func (fs *FS) NewQid(statMode uint32) proto.Qid {
	fs.Lock()
	defer fs.Unlock()
	uid := fs.uid
	fs.uid = fs.uid + 1
	return proto.Qid{
		Qtype: uint8(statMode >> 24),
		Vers:  0,
		Uid:   uid,
	}
}

// NewStat creates and returns a new proto.Stat object for use with a
// FSNode. name will be the name of the node, and it will be owned by
// user uid and group gid. mode is standard unix permissions bits, along
// with any special mode bits (e.g. proto.DMDIR for directories)
func (fs *FS) NewStat(name, uid, gid string, mode uint32) *proto.Stat {
	return &proto.Stat{
		Type:   0,
		Dev:    0,
		Qid:    fs.NewQid(mode),
		Mode:   mode,
		Atime:  uint32(time.Now().Unix()),
		Mtime:  uint32(time.Now().Unix()),
		Length: 0,
		Name:   name,
		Uid:    uid,
		Gid:    gid,
		Muid:   uid,
	}
}

// RMFile is a function intended to be used with the WithRemoveFile Option.
// RMFile simply enables the deletion of files and directories on the
// FS subject to usual permissions checks.
func RMFile(fs *FS, f FSNode) error {
	parent := f.Parent()
	parent.DeleteChild(f.Stat().Name)
	return nil
}

// Options are used to configure an FS with NewFS
type Option func(*FS)

// WithCreateFile configures a function to be called when a client attempts
// to create a file on the FS. The function f, if successful, should return
// a File, which it should add to the parent Dir. If a file should not be
// created, f can return an error which will be sent to the client.
// Basic permissions checking is done by the FS before calling f, but any
// other checking can be done by f.
func WithCreateFile(f func(fs *FS, parent Dir, user, name string, perm uint32, mode uint8) (File, error)) Option {
	return func(fs *FS) {
		fs.CreateFile = f
	}
}

// WithCreateDir configures a function to be called when a client attempts
// to create a directory on the FS. The function f, if successful, should return
// a Dir, which it should add to the parent Dir. If a file should not be
// created, f can return an error which will be sent to the client.
// Basic permissions checking is done by the FS before calling f, but any
// other checking can be done by f.
func WithCreateDir(f func(fs *FS, parent Dir, user, name string, perm uint32, mode uint8) (Dir, error)) Option {
	return func(fs *FS) {
		fs.CreateDir = f
	}
}

// WithRemoveFile configures a function to be called when a client attempts
// to remove a file or directory from the FS. The function f, if successful,
// should remove the FSNode from its parent Dir, and return nil. If f does
// not choose to remove the FSNode, it should return an error, which will
// be sent to the client.
func WithRemoveFile(f func(fs *FS, f FSNode) error) Option {
	return func(fs *FS) {
		fs.RemoveFile = f
	}
}

// WithWalkFailHandler configures a function to be called when a client attempts
// to walk a path on the FS that does not exist. The function f may decide
// to create a File or Directory on the fly, which should be returned.
// If f chooses not to create an FSNode to satisfy the walk, an error
// may be returned which will be sent to the client.
func WithWalkFailHandler(f func(fs *FS, parent Dir, name string) (FSNode, error)) Option {
	return func(fs *FS) {
		fs.WalkFail = f
	}
}
