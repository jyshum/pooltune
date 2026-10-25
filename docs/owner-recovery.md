# Offline local owner recovery

`pooltune-owner` is separate from `pooltune-admin`, which manages hosted gateway
accounts. Owner recovery operates only on an existing initialized control database.
It does not create a new installation, reset policy history, lift safety ceilings,
reconcile database work, or open the gateway accounting database.

Stop the control process first. Leave traffic running if it is healthy. The command
must acquire the same exclusive 0700 control-directory lock as the service; it
rejects while control owns that lock. This is an offline filesystem-owner action,
not a public password-reset endpoint or a recovery option for a remote browser.
Keep the control directory and its backups private. A compromised filesystem owner
already controls local credentials and is outside the browser authentication boundary.

## Recover a claimed administrator password

Write the new password to a private regular file beneath an approved secret root.
Use one line with 12 to 72 bytes. The file must have mode 0600; do not pass a
password on the command line or through chat.

```sh
pooltune-owner reset-password \
  -control-directory /absolute/private/control \
  -secret-root /absolute/private/secrets \
  -password-file /absolute/private/secrets/new-password
```

The command hashes the password using bcrypt cost 12, changes the existing owner,
revokes every session, and appends `password_recovered` in one SQLite transaction.
An audit write failure rolls back both password and session changes. It never
reopens bootstrap setup. Restart control, sign in with the new password, then
protect or remove the input password file. No password or session token is printed.

## Replace a lost or expired unclaimed bootstrap token

```sh
pooltune-owner replace-claim \
  -control-directory /absolute/private/control \
  -claim-file /absolute/private/secrets/replacement-claim \
  -valid-for 1h
```

The output directory must be 0700 and the output file must not already exist.
The command generates a new random token, writes and fsyncs its 0600 file and
parent directory, then atomically replaces the database hash/expiry and appends
`claim_replaced`. The old token stops working. The lifetime is at most 24 hours.
It rejects already claimed installations. A failed database mutation removes the
new file during normal error cleanup; interruption before commit may leave an
unused private file that can be inspected and replaced using a new filename.

Set `Admin.ClaimFile` in the control profile to the new path beneath its secret
root, then restart control. The stored expiry remains authoritative; restarting
cannot extend it. Recovery never prints the token or overwrites an existing file.

Tests cover exclusive ownership, secret-file permissions, old-password/session
revocation, claim replacement, immutable audit history, audit-failure rollback,
and rejection of unrelated or missing databases. Customer/remote administrator
release and the broader G2 threat-model review remain open.
