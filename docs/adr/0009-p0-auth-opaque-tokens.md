# 0009. P0 authentication: opaque, hashed bearer tokens

Status: accepted, 29 Sep 2026

## Context

P0 needs owners and devices to call the API. Full owner login (OTP, sessions, staff PINs, roles) belongs to the identity module proper.

## Decision

Owners get a token at signup, devices at pairing: 256 random bits, base64url, prefixed `onx_o_` or `onx_d_` so secret scanners can find leaks. Only the SHA-256 is stored. Pairing codes are 8 characters from a 31-letter alphabet, single-use, valid 10 minutes, hashed at rest, and redeemed atomically inside the device-creation transaction.

## Consequences

Simple and safe enough for development and a pilot behind rate limiting. Not the final design: no expiry, no rotation, no per-staff identity yet.

## Alternatives considered

JWTs (rejected for now: revocation needs a lookup anyway).
