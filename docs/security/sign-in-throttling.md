# Sign-in throttling and hash admission

This document describes how `POST /session` resists password guessing and
sign-in floods. The decision record is
[ADR 0010](../decisions/0010-authentication-amendments.md); the architecture
summary is `ARCHITECTURE.md` §19.2 and §19.5. Implemented by SFT-104 (audit
findings SEC-02 and SEC-10).

## Threats

- **Parallel guessing.** An attacker sends many wrong passwords at once. When
  the throttle only read its counters and counted a failure after the
  Argon2id hash, N parallel requests got N guesses before the fifth-failure
  delay applied.
- **Hash starvation.** Argon2id is deliberately expensive (32 MiB, three
  iterations), and at most two hashes run at once. When the wait for a hash
  slot was unbounded, a flood from rotating addresses queued ahead of the
  real administrator, who could not sign in until the flood stopped.
- **Address rotation.** One IPv6 subscriber usually controls a whole /64,
  which is 2^64 addresses. A key per full IPv6 address gave such an attacker
  an unlimited supply of fresh client keys.
- **Remote lockout.** The account key is the username hash and does not
  depend on the client. One failure a minute from anywhere kept the account
  delayed indefinitely, and the only way out was the local CLI.

## Admission

Every sign-in attempt that would hash a password is admitted first. This
includes the dummy hash for an unknown or disabled username. The order is:

1. **Throttle reservation.** `Check` reserves the attempt for the client key
   and the account key and counts it as a failure at once. A wrong password
   keeps the count. A success clears both keys. An attempt whose password
   was never evaluated, such as an admission overflow or a storage error, is
   withdrawn and not counted. A key that is inside its retry delay gets `429`
   with that delay before any hashing.
2. **One in flight per key.** A second attempt for a client key or account
   key that already has one in flight gets `429` with `Retry-After: 1` at
   once. No hash and no per-request audit row result. Together with the reservation,
   parallel requests therefore cannot multiply guesses. Each key evaluates at
   most one password at a time, and every evaluated password is counted
   before the next one can start.
3. **Bounded hash queue.** The two hash slots are shared by all callers.
   Sign-in attempts wait in one of two bounded FIFO lanes, each for at most
   2 seconds:
   - the **general lane** holds up to 8 waiters;
   - the **recent-client lane** holds up to 2 waiters, for client keys that
     signed in successfully in the last 7 days. A freed slot always goes to
     this lane first.

   A full lane, or an expired wait, gets `429` with `Retry-After: 2`
   immediately, without a hash and without a per-request audit row. Administrator
   creation and password reset by the local operator use an unbounded
   internal lane that waits until its request ends.

Client keys are taken after trusted-proxy resolution. IPv6 addresses are
grouped by /64 (zone removed) and IPv4 addresses by /32. An IPv4-mapped IPv6
address counts as its IPv4 address. A peer address that cannot be parsed
falls back to the key `unknown`.

Retry delays are unchanged. The fifth counted failure on a key sets a
1-second delay. Each further failure doubles it, up to 60 seconds.

## Account lockout exemption

The account key still counts failures from every client. It no longer blocks
a client key that signed in successfully in the last 7 days. Neither the
account's retry delay nor its in-flight attempt applies to such a client. That
client is still subject to its own client throttle and its own in-flight
limit. Its failures still count toward the account key, so they still delay
other clients.

The effect is that a remote attacker cannot keep the administrator locked
out from a network the administrator recently signed in from. During a flood,
such a client waits at most one hash duration plus one other recent-lane
waiter, and it can never be rejected because the general lane overflowed.

## Measured values

On the development machine (4 cores), one Argon2id hash takes about 100 ms.
Six hashes through the two slots took 0.31 s. A full general lane of 8
therefore drains in about 0.4 s, so the 2-second wait leaves about five times
headroom for slower hardware before waiting attempts expire. In the flood
test (`TestLoginFloodLeavesRecentAdministratorAbleToSignIn`), 24 workers sent
about 14,800 requests from rotating IPv6 /64s. Most of them were refused at
once as in-flight or overloaded. The recently successful administrator still
signed in after about 140 ms, or about 270 ms under the race detector. The
values are constants, not configuration.

## Tradeoffs and limitations

- **Restarts forget recent clients.** The recent-client set lives only in
  memory. After a restart it is empty, and until the administrator's next
  successful sign-in they compete in the general lane. During a sustained
  flood that targets the administrator's username, an unproven client is
  delayed by the account throttle and can get overload `429`s. Recovery is
  to wait for the flood to stop or to sign in from a network that is not
  under the attacker's account delay. The local CLI is never affected.
- **Shared networks share a key.** Clients behind one IPv4 NAT, or on one
  IPv6 /64, share one client key, one in-flight slot, and one retry delay.
  An attacker on the administrator's own /64 or NAT shares the
  administrator's recent-client exemption. That attacker is still limited by
  the shared client throttle, which delays the administrator too.
- **The recent set is bounded.** It holds at most 64 client keys, which is the
  session cap. Beyond that, the oldest success is forgotten first.
- **In-memory state.** Throttle entries are bounded at 4,096 idle keys and
  expire after 15 minutes idle. An entry with an attempt in flight is never
  evicted, so a burst of fresh keys cannot erase another key's in-flight
  limit.
- **Auditing.** No rejected attempt writes during its request. Wrong
  credentials, throttled, in-flight, and overloaded attempts are counted in
  memory and recorded as at most one `sign_in.rejected_summary` row per
  5-minute window, with per-reason counts, a distinct-client count, and the
  most frequent client key ([audit.md](audit.md)).
