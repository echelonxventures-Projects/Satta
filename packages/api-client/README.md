# @satta/api-client

Generated TypeScript client for the platform's published contracts, produced
from `proto/` by the same protobuf source that generates the Go services
(`task generate:ts`). Apps must not hand-write wire types; drift here is
drift at the contract boundary.

## Generated code

`src/gen/` is committed (consumers need it without running codegen) and is
**never hand-edited**. `task gen:check` regenerates and fails if the committed
tree drifts from `proto/` — the check that makes "one contract, many
consumers" real rather than aspirational.

## Transport

Clients are built with `@connectrpc/connect`'s `createClient` over either:

- `createGrpcTransport` (`@connectrpc/connect-node`) — direct gRPC to a
  service; used by Node tooling, B2B partners and tests today (the template
  service serves gRPC directly), and
- browser transports — Connect/gRPC-web through the API gateway (plan S12a,
  not yet built). Until the gateway exists, player web surfaces talk to REST
  adapter endpoints, and this package stays transport-agnostic.

Browser-origin traffic to a raw gRPC server is not a thing; saying so here
prevents someone "fixing" it by shipping a hand-rolled fetch shim.

## Versioning

Wire compatibility is enforced by `task breaking` (buf) in CI across every
language's generated code. A change that breaks this package's generated
types breaks Go consumers too — that is the point of the shared source.
