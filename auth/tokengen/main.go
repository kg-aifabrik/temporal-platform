// tokengen generates the signing key, JWKS, and demo JWTs for the LOCAL
// JWT-RBAC setup. Standard library only — no external dependencies.
//
// It mints RS256 tokens whose `permissions` claim uses Temporal's built-in
// format ["<namespace>:<role>"]. Temporal's default JWT claim mapper reads
// that claim; the default authorizer then enforces it per API call:
//
//	temporal-system:read   -> Reader on ALL namespaces (read everything)
//	<namespace>:write      -> Writer on that namespace (modify own team)
//	temporal-system:admin  -> Admin everywhere (incl. delete)
//
// Workers need <namespace>:write because the poll/respond APIs are AccessWrite.
//
// Outputs (all gitignored) into -out (default ./out):
//
//	jwt-private.pem      the RSA private key (kept only to re-sign; never leaves local)
//	jwks.json            the public key set the Temporal frontend fetches
//	tokens/<name>.jwt    one bearer token per identity
//
// Run:  go run ./auth/tokengen            (from repo root: go run . inside this dir)
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha256" // register SHA-256 for crypto.SHA256.New()
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const kid = "local-signing-key"

// identity is a token subject and the permissions it should carry.
type identity struct {
	name        string
	sub         string
	permissions []string
}

// The four team members, one admin, and the two per-team worker service tokens.
var identities = []identity{
	{"alice", "alice@corp.local", []string{"temporal-system:read", "team-a:write"}},
	{"bob", "bob@corp.local", []string{"temporal-system:read", "team-a:write"}},
	{"carol", "carol@corp.local", []string{"temporal-system:read", "team-b:write"}},
	{"dave", "dave@corp.local", []string{"temporal-system:read", "team-b:write"}},
	{"admin", "admin@corp.local", []string{"temporal-system:admin"}},
	{"worker-team-a", "worker-team-a", []string{"team-a:write"}},
	{"worker-team-b", "worker-team-b", []string{"team-b:write"}},
}

func main() {
	out := flag.String("out", "out", "output directory")
	flag.Parse()

	if err := os.MkdirAll(filepath.Join(*out, "tokens"), 0o755); err != nil {
		log.Fatal(err)
	}

	// Reuse an existing key if present so tokens and JWKS stay consistent across
	// re-runs; otherwise generate a fresh 2048-bit RSA key.
	key := loadOrCreateKey(filepath.Join(*out, "jwt-private.pem"))

	writeFile(filepath.Join(*out, "jwks.json"), jwks(&key.PublicKey))

	exp := time.Now().Add(365 * 24 * time.Hour)
	for _, id := range identities {
		tok := signJWT(key, id.sub, id.permissions, exp)
		writeFile(filepath.Join(*out, "tokens", id.name+".jwt"), []byte(tok))
		fmt.Printf("%-15s %s\n", id.name, id.permissions)
	}
	fmt.Printf("\nJWKS + %d tokens written to %s/\n", len(identities), *out)
}

// --- JWT (RS256) and JWKS, built from the standard library ---

func signJWT(key *rsa.PrivateKey, sub string, permissions []string, exp time.Time) string {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	claims := map[string]any{
		"sub":         sub,
		"permissions": permissions,
		"iat":         time.Now().Unix(),
		"exp":         exp.Unix(),
	}
	signingInput := b64(mustJSON(header)) + "." + b64(mustJSON(claims))
	digest := sha256sum(signingInput)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest)
	if err != nil {
		log.Fatal(err)
	}
	return signingInput + "." + b64(sig)
}

func jwks(pub *rsa.PublicKey) []byte {
	e := big.NewInt(int64(pub.E)).Bytes()
	key := map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(e),
	}
	return mustJSON(map[string]any{"keys": []any{key}})
}

// --- helpers ---

func loadOrCreateKey(path string) *rsa.PrivateKey {
	if pemBytes, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(pemBytes)
		if block != nil {
			if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return k
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	writeFile(path, pemBytes)
	return key
}

func sha256sum(s string) []byte {
	h := crypto.SHA256.New()
	h.Write([]byte(s))
	return h.Sum(nil)
}

func b64(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func mustJSON(v any) []byte        { b, err := json.Marshal(v); must(err); return b }
func writeFile(p string, b []byte) { must(os.WriteFile(p, b, 0o600)) }
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
