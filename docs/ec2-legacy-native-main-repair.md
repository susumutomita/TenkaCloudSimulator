# Repair a legacy native VPC's main route table

New native VPCs already have a main table and read-only IPv4 `local` routes.
For a native VPC created before that storage change, the operator can repair one
exact VPC ID in one exact region. This is local simulator maintenance, not an AWS
API action. There is no startup migration, repair on a read, or repair action in
the Console. A CloudFormation-managed VPC is not eligible.

## Keep the original and repair a separate copy

The command is available only in stock Linux and macOS builds with the supported
SQLite file locking. Stop the simulator and any other process using the state
before copying it. Use an operator-owned private directory with stable, trusted
path ancestors
(for example `0700`) and files with `0400` or `0600` permissions. Version 1 accepts
only existing SQLite schema-version-1 rollback-journal state up to 256 MiB.
It refuses WAL, sidecar journals, file symlinks, hard links, and an original/target
alias. The command does not locate or stop other processes for you.

1. Keep the stopped original intact. Place a byte-identical, separately owned
   working file in a private directory and make the working file `0600`.
2. Run `plan` against the original. Inspect the region, VPC ID, two system IDs,
   and exact list of existing native tables. `ready` reports one additional row;
   stop if the actual VPC or table list is not the intended scope.
3. Copy the `planDigest` value into `--expect-plan` and apply to the working
   file, not to the original. `applied` is issued only after SQLite confirms
   commit and a new read-only connection verifies the saved state. A repeat with
   the same unchanged original, scope, and digest returns `already-repaired`
   without writing. Use `plan` to check state after an interrupted or uncertain
   command; do not replay blindly.
4. Start the current simulator with `--state` set to the verified working
   file. `DescribeRouteTables` and the normal authenticated Console can now
   show the repaired VPC's main table, its read-only IPv4 `local` routes, and
   the same previous routes and explicit associations.

```sh
tenkacloud-simulator repair-ec2-native-main-route plan \
  --state "$original" --region us-east-1 --vpc-id "$vpc_id"
tenkacloud-simulator repair-ec2-native-main-route apply \
  --state "$working_copy" --original "$original" \
  --region us-east-1 --vpc-id "$vpc_id" --expect-plan "$plan_digest"
```

Apply rechecks the original and copy, exact typed native ownership and absence
of conflicting CloudFormation claims and declarations, and the shared
1,000-network-row bound. Unexpected network-table triggers and dependent
foreign keys that can change on update or reference mutable network columns
are refused. It updates the selected parent and existing custom
table generations in one bounded exclusive transaction, adds one stored main
table, and verifies all net-row bytes apart from those planned changes. The
system association has no subnet ID; it and the `local` routes cost no extra
stored route rows. Existing tags, subnet and explicit association IDs, static
routes, route-create replay records, IAM, other VPCs and CloudFormation are not
rewritten. An implicit subnet still does not match `association.subnet-id`.

For rollback, stop the simulator and select the untouched original or a new
copy of it. Never run a simulator version older than the repair against the
repaired state. This config-only repair does not replace a main table, add
secondary CIDRs, or implement packet routing, reachability or AWS parity.

The [pinned SDK and served Console fixture](../test/console/ec2_legacy_main_repair.py)
creates a disposable state using a separate signed pre-change binary, applies
to its copy, checks both actual SQLite images, and reopens the result. Its
Console check uses jsdom, not browser layout or forwarding.
