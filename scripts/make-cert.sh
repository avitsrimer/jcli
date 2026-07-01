#!/usr/bin/env bash
# make-cert.sh — create OR reuse a self-signed code-signing certificate in the
# macOS login keychain, used by `make sign` to sign the jcli binary.
#
# WARNING: this script is idempotent ON PURPOSE. If a certificate with the
# configured common-name already exists it exits 0 WITHOUT touching the keychain.
# NEVER regenerate the certificate: a new cert produces a new designated
# requirement (DR), which invalidates the Keychain item's trusted-app ACL — the
# keychain "Allow / Always Allow" authorization prompt reappears and any prior
# "Always Allow" trust is lost.
# To rotate the identity you must also re-store every keychain token item.
set -euo pipefail

CERT_CN="${CERT_CN:-jcli Code Signing}"
KEYCHAIN="${KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "make-cert.sh: only supported on macOS (Darwin)" >&2
	exit 1
fi

# idempotency guard: reuse an existing identity, never regenerate it.
if security find-certificate -c "$CERT_CN" "$KEYCHAIN" >/dev/null 2>&1; then
	echo "make-cert.sh: certificate \"$CERT_CN\" already exists — reusing (will NOT regenerate)."
	exit 0
fi

echo "make-cert.sh: creating self-signed code-signing certificate \"$CERT_CN\"..."

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

key="$workdir/key.pem"
crt="$workdir/cert.pem"
p12="$workdir/identity.p12"
ext="$workdir/codesign.ext"
# random passphrase for the transient PKCS#12 bundle; never persisted.
p12pass="$(openssl rand -hex 16)"

# extendedKeyUsage = codeSigning is what makes the identity usable by codesign.
cat >"$ext" <<'EOF'
basicConstraints=critical,CA:false
keyUsage=critical,digitalSignature
extendedKeyUsage=critical,codeSigning
EOF

# generate a key + self-signed leaf certificate carrying the codeSigning EKU.
openssl req -x509 -newkey rsa:2048 -nodes \
	-keyout "$key" -out "$crt" \
	-days 3650 \
	-subj "/CN=$CERT_CN" \
	-extensions v3 -config <(cat <<EOF
[ req ]
distinguished_name = dn
x509_extensions = v3
prompt = no
[ dn ]
CN = $CERT_CN
[ v3 ]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
)

# bundle key + cert into a PKCS#12 so `security import` brings in the private key.
# -legacy forces 3DES/RC2 + SHA1-MAC: OpenSSL 3 defaults to a PBES2/SHA-256 MAC
# that macOS `security import` cannot parse (it fails as a bogus "MAC verification
# failed / wrong password" error), so the legacy encoding is required here.
openssl pkcs12 -export -legacy -inkey "$key" -in "$crt" -out "$p12" \
	-name "$CERT_CN" -passout "pass:$p12pass"

# import into the login keychain; -T grants codesign access without a prompt
# on every signing operation.
security import "$p12" \
	-k "$KEYCHAIN" \
	-P "$p12pass" \
	-T /usr/bin/codesign

echo "make-cert.sh: imported \"$CERT_CN\" into $KEYCHAIN."
echo
echo "Optional, for fully unattended signing, mark it trusted for codesigning"
echo "(needs sudo and is interactive on some macOS versions):"
echo
echo "    sudo security add-trusted-cert -d -r trustRoot \\"
echo "        -p codeSign -k /Library/Keychains/System.keychain \"$crt\""
echo
echo "Note: the cert PEM lives in a temp dir wiped on exit; re-run is a no-op"
echo "because the identity is now present. Do NOT regenerate (see header)."
