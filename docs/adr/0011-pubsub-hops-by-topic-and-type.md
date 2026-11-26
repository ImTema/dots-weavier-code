# Pub/Sub hops match on topic (sure) or message type (guess)

Pub/Sub ends reuse `graph.Calls` and `graph.Impls` under their own key prefixes, so they are stored and joined like gRPC.

- **Topic key, sure hop.** The topic is bound where the `*pubsub.Publisher` or the subscription is made, never at `Publish`/`Receive`, so it is followed through the struct field that holds the client.
- **Type key, guess hop.** The proto type marshalled next to `Publish` is matched with the one a subscriber project unmarshals. One type can travel on several topics, so a type match alone is only a guess.

The cost of the type key is guess hops between services that share a message type but not a topic.
