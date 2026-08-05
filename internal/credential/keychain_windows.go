//go:build windows

package credential

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows backend calls the Credential Manager API directly rather than
// shelling out to cmdkey. cmdkey can write a credential but cannot read its
// secret back, which makes it useless as a store. These are plain syscalls into
// advapi32, so no cgo is involved and the single-binary distribution holds.
var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2

	// errNotFound is ERROR_NOT_FOUND, returned when the target name has no
	// stored credential.
	errNotFound = windows.Errno(1168)
)

// credentialW mirrors the CREDENTIALW structure. Field order and types must
// match the C layout exactly; the API writes into this memory.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// keychainStore talks to the Windows Credential Manager.
type keychainStore struct{}

var _ Store = (*keychainStore)(nil)

func newKeychainStore() Store { return &keychainStore{} }

func (s *keychainStore) Name() string { return "keychain" }

// keychainAvailable reports whether advapi32 exposes the credential API. It
// always should on a real Windows host; the check guards against stripped
// container images.
func keychainAvailable() bool {
	return procCredReadW.Find() == nil && procCredWriteW.Find() == nil
}

// targetName namespaces our entries inside the user's credential store.
func targetName(key string) string { return service + ":" + key }

func (s *keychainStore) Get(_ context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	target, err := windows.UTF16PtrFromString(targetName(key))
	if err != nil {
		return "", fmt.Errorf("encode credential target: %w", err)
	}

	var cred *credentialW
	ret, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if ret == 0 {
		if callErr == errNotFound {
			return "", fmt.Errorf("%w: %q is not in the credential manager", ErrNotFound, key)
		}
		return "", fmt.Errorf("credential manager lookup for %q: %w", key, callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))

	if cred.CredentialBlobSize == 0 || cred.CredentialBlob == nil {
		return "", fmt.Errorf("%w: %q has an empty secret", ErrNotFound, key)
	}

	// The blob is written as UTF-16, matching what Set stores below.
	blob := unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)
	utf16Chars := unsafe.Slice((*uint16)(unsafe.Pointer(&blob[0])), len(blob)/2)
	return windows.UTF16ToString(utf16Chars), nil
}

func (s *keychainStore) Set(_ context.Context, key, secret string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateSecret(secret); err != nil {
		return err
	}

	target, err := windows.UTF16PtrFromString(targetName(key))
	if err != nil {
		return fmt.Errorf("encode credential target: %w", err)
	}
	user, err := windows.UTF16PtrFromString(service)
	if err != nil {
		return fmt.Errorf("encode credential user: %w", err)
	}

	// UTF16FromString appends a NUL terminator that is not part of the
	// secret, so it is dropped before measuring the blob.
	encoded, err := windows.UTF16FromString(secret)
	if err != nil {
		return fmt.Errorf("encode credential secret: %w", err)
	}
	encoded = encoded[:len(encoded)-1]

	cred := credentialW{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(encoded) * 2),
		CredentialBlob:     (*byte)(unsafe.Pointer(&encoded[0])),
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}

	ret, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if ret == 0 {
		return fmt.Errorf("credential manager write for %q: %w", key, callErr)
	}
	// encoded must stay alive until the call returns, since cred holds a raw
	// pointer into it that the garbage collector cannot see.
	runtime.KeepAlive(encoded)
	return nil
}

func (s *keychainStore) Delete(_ context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	target, err := windows.UTF16PtrFromString(targetName(key))
	if err != nil {
		return fmt.Errorf("encode credential target: %w", err)
	}

	ret, _, callErr := procCredDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if ret == 0 {
		if callErr == errNotFound {
			return fmt.Errorf("%w: %q is not in the credential manager", ErrNotFound, key)
		}
		return fmt.Errorf("credential manager delete for %q: %w", key, callErr)
	}
	return nil
}
