# Reusable provider transports

This package owns the wire encoders, decoders, streaming parsers and media operations extracted from Gaoge's 38 protocol adapters. It does not select user routes, decrypt credentials, authorize media, bill users, persist requests or retry unknown external effects.

The Host supplies an explicit HTTPClientFactory. Preserve its SSRF controls, network policy and observability wrapper; a missing factory fails closed. RouteConfig carries credentials only in memory. GenerateInput is transient wire input, not a second durable Runtime request. The Host projects the sealed model.Request and resolves immutable authorized artifact references immediately before dispatch.

Text, image, video, audio, world/3D, embeddings and rerank operations retain their distinct operation contracts. Media payload bytes belong only in this transient transport boundary. Native token counting uses the same protocol encoding and never falls back to generation.

All original deterministic protocol fixture tests are retained here. Host Gateway conversion assertions remain a responsibility of the Gaoge integration. Passing fixtures does not certify live provider compatibility or exactly-once effects.
