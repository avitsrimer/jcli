//go:build darwin

package agent

/*
#cgo CFLAGS: -x objective-c -fno-objc-arc
#cgo LDFLAGS: -framework Security -framework Foundation -framework CoreFoundation
#include <stdlib.h>
#include <Security/Security.h>

// makeData wraps a Go byte slice as a CFDataRef the caller must release.
static CFDataRef makeData(const void *bytes, int len) {
	return CFDataCreate(kCFAllocatorDefault, (const UInt8 *)bytes, len);
}

// makeString wraps a NUL-terminated C string as a CFStringRef the caller must release.
static CFStringRef makeString(const char *s) {
	return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

// setItem stores token under service/account as a plain generic-password item. It first deletes any
// existing item, then adds a fresh one with no access-control and no accessibility attribute, so the
// item lands in the default (login) keychain and the creating signed binary is added to its ACL.
// Returns the OSStatus (errSecSuccess on success).
static int setItem(const char *service, const char *account, const void *token, int tokenLen) {
	CFStringRef svc = makeString(service);
	CFStringRef acct = makeString(account);
	CFDataRef tok = makeData(token, tokenLen);

	// delete any prior item so the value is overwritten cleanly.
	CFMutableDictionaryRef del = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(del, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(del, kSecAttrService, svc);
	CFDictionarySetValue(del, kSecAttrAccount, acct);
	SecItemDelete(del);
	CFRelease(del);

	CFMutableDictionaryRef add = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(add, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(add, kSecAttrService, svc);
	CFDictionarySetValue(add, kSecAttrAccount, acct);
	CFDictionarySetValue(add, kSecValueData, tok);

	OSStatus status = SecItemAdd(add, NULL);

	CFRelease(add);
	CFRelease(tok);
	CFRelease(acct);
	CFRelease(svc);
	return (int)status;
}

// getItem reads the token for service/account with a plain SecItemCopyMatching. The creating signed
// binary is trusted by the item's ACL and reads it back silently; a binary with a different code
// signature triggers the standard keychain authorization prompt. On success it returns errSecSuccess
// and sets *outData / *outLen to a malloc'd buffer the caller must free.
static int getItem(const char *service, const char *account, void **outData, int *outLen) {
	CFStringRef svc = makeString(service);
	CFStringRef acct = makeString(account);

	CFMutableDictionaryRef q = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, svc);
	CFDictionarySetValue(q, kSecAttrAccount, acct);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);

	CFDataRef result = NULL;
	OSStatus status = SecItemCopyMatching(q, (CFTypeRef *)&result);
	CFRelease(q);
	CFRelease(acct);
	CFRelease(svc);

	if (status == errSecSuccess && result != NULL) {
		CFIndex n = CFDataGetLength(result);
		void *buf = malloc((size_t)n);
		if (buf != NULL) {
			CFDataGetBytes(result, CFRangeMake(0, n), (UInt8 *)buf);
			*outData = buf;
			*outLen = (int)n;
		} else {
			status = errSecAllocate;
		}
	}
	if (result != NULL) {
		CFRelease(result);
	}
	return (int)status;
}

// deleteItem removes the item for service/account. errSecItemNotFound is treated as success by
// the Go caller. Returns the OSStatus.
static int deleteItem(const char *service, const char *account) {
	CFStringRef svc = makeString(service);
	CFStringRef acct = makeString(account);

	CFMutableDictionaryRef del = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(del, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(del, kSecAttrService, svc);
	CFDictionarySetValue(del, kSecAttrAccount, acct);

	OSStatus status = SecItemDelete(del);
	CFRelease(del);
	CFRelease(acct);
	CFRelease(svc);
	return (int)status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// keychainService is the generic-password service string. It shows in the keychain authorization
// prompt that a different-DR binary triggers, so it deliberately reads as the product name.
const keychainService = "Jenkins CLI"

// errSecItemNotFound mirrors the Security framework status for a missing item.
const errSecItemNotFound = -25300

// errSecMissingEntitlement is returned when the calling binary is unsigned or ad-hoc signed.
const errSecMissingEntitlement = -34018

// osStatusHint returns an actionable explanation for keychain OSStatus codes with a known cause, or
// an empty string for codes without a specific hint.
func osStatusHint(status int) string {
	switch status {
	case errSecMissingEntitlement:
		return " (errSecMissingEntitlement: the jcli binary must be code-signed with a real identity, not ad-hoc — run 'make sign')"
	default:
		return ""
	}
}

// darwinKeychain is the cgo-backed keychainStore. The token is stored as a plain generic-password
// item in the default (login) keychain; the item's trusted-application ACL is bound to the signed
// binary's designated requirement, so the same signed binary reads it back without a prompt. It is
// not safe for concurrent use; the agent serialises access through its request loop.
type darwinKeychain struct{}

// newKeychainStore returns the platform keychainStore for darwin.
func newKeychainStore() (keychainStore, error) {
	return &darwinKeychain{}, nil
}

// account derives the per-profile keychain account string.
func account(profile string) string {
	return "jcli:" + profile
}

// Set stores token for profile as a plain generic-password item in the default (login) keychain. It
// does not prompt.
func (k *darwinKeychain) Set(profile, token string) error {
	cSvc := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cSvc))
	cAcct := C.CString(account(profile))
	defer C.free(unsafe.Pointer(cAcct))

	tok := []byte(token)
	var tokPtr unsafe.Pointer
	if len(tok) > 0 {
		tokPtr = unsafe.Pointer(&tok[0])
	}
	status := C.setItem(cSvc, cAcct, tokPtr, C.int(len(tok)))
	if int(status) != 0 {
		return fmt.Errorf("keychain set for profile %q failed: OSStatus %d%s", profile, int(status), osStatusHint(int(status)))
	}
	return nil
}

// Get reads the token for profile. The signed binary that created the item is trusted by its ACL and
// reads it back silently; a binary with a different signature hits the standard keychain prompt.
func (k *darwinKeychain) Get(profile string) (string, error) {
	cSvc := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cSvc))
	cAcct := C.CString(account(profile))
	defer C.free(unsafe.Pointer(cAcct))

	var data unsafe.Pointer
	var n C.int
	status := C.getItem(cSvc, cAcct, &data, &n)
	if int(status) != 0 {
		if int(status) == errSecItemNotFound {
			return "", fmt.Errorf("keychain get for profile %q: %w", profile, ErrNoToken)
		}
		return "", fmt.Errorf("keychain get for profile %q failed: OSStatus %d%s", profile, int(status), osStatusHint(int(status)))
	}
	if data == nil {
		return "", fmt.Errorf("keychain get for profile %q: %w", profile, ErrNoToken)
	}
	defer C.free(data)
	token := C.GoStringN((*C.char)(data), n)
	return token, nil
}

// Delete removes the item for profile. A missing item is reported as success.
func (k *darwinKeychain) Delete(profile string) error {
	cSvc := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cSvc))
	cAcct := C.CString(account(profile))
	defer C.free(unsafe.Pointer(cAcct))

	status := C.deleteItem(cSvc, cAcct)
	if int(status) != 0 && int(status) != errSecItemNotFound {
		return fmt.Errorf("keychain delete for profile %q failed: OSStatus %d%s", profile, int(status), osStatusHint(int(status)))
	}
	return nil
}
