import { Centrifuge, type Subscription } from "centrifuge";
import { BehaviorSubject, Observable, shareReplay, map, filter } from "rxjs";
import {
  LiveChannelConnectionState,
  LiveChannelEventType,
  LoadingState,
  StreamingDataFrame,
  toLiveChannelId,
  type LiveChannelAddress,
  type LiveChannelEvent,
  type DataFrameJSON,
} from "@grafana/data";
import type { GrafanaLiveSrv, LiveDataStreamOptions } from "@grafana/runtime";
import { api } from "../api";

type Entry = {
  metadata: string;
  observable: Observable<LiveChannelEvent>;
  subscription?: Subscription;
};
function metadataKey(value: unknown): string {
  return JSON.stringify(value ?? null, (_key, v) =>
    v && typeof v === "object" && !Array.isArray(v)
      ? Object.fromEntries(
          Object.entries(v).sort(([a], [b]) => a.localeCompare(b)),
        )
      : v,
  );
}
export function createLiveService(): GrafanaLiveSrv {
  const connections = new BehaviorSubject(false),
    entries = new Map<string, Entry>();
  let client: Centrifuge | undefined;
  function connection() {
    if (client) return client;
    const url = new URL("/api/live/ws", location.origin);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const c = new Centrifuge(url.toString(), {
      // This hook runs before opening each transport, including reconnects
      // after sleep. No timers or token-bearing WebSocket URLs are needed.
      getData: async () => {
        await api("/api/live/session", { method: "POST" });
        return {};
      },
    });
    c.on("connected", () => connections.next(true));
    c.on("connecting", () => connections.next(false));
    c.on("disconnected", () => connections.next(false));
    c.on("error", () => {});
    client = c;
    c.connect();
    return c;
  }
  function getStream<T>(
    address: LiveChannelAddress,
  ): Observable<LiveChannelEvent<T>> {
    const id = toLiveChannelId(address),
      metadata = metadataKey(address.data);
    return new Observable<LiveChannelEvent<T>>((observer) => {
      let entry = entries.get(id);
      if (entry && entry.metadata !== metadata) {
        observer.error(
          new Error("Channel metadata differs; use a different stream path"),
        );
        return;
      }
      if (!entry) {
        const created: Entry = {
          metadata,
          observable: new Observable<LiveChannelEvent>((subscriber) => {
            let disposed = false,
              c: Centrifuge | undefined,
              subscription: Subscription | undefined;
            const status = (
              state: LiveChannelConnectionState,
              message?: unknown,
              error?: unknown,
            ) =>
              subscriber.next({
                type: LiveChannelEventType.Status,
                id,
                timestamp: Date.now(),
                state,
                message,
                error,
              });
            status(LiveChannelConnectionState.Pending);
            try {
              c = connection();
              subscription = c.newSubscription(id, {
                data: address.data,
                joinLeave: true,
              });
              created.subscription = subscription;
              subscription.on("subscribing", () =>
                status(LiveChannelConnectionState.Connecting),
              );
              subscription.on("subscribed", (event) =>
                status(LiveChannelConnectionState.Connected, event.data),
              );
              subscription.on("publication", (event) =>
                subscriber.next({
                  type: LiveChannelEventType.Message,
                  message: event.data,
                }),
              );
              subscription.on("join", (event) =>
                subscriber.next({
                  type: LiveChannelEventType.Join,
                  user: event.info,
                }),
              );
              subscription.on("leave", (event) =>
                subscriber.next({
                  type: LiveChannelEventType.Leave,
                  user: event.info,
                }),
              );
              subscription.on("error", (event) => {
                status(
                  LiveChannelConnectionState.Connecting,
                  undefined,
                  event.error,
                );
              });
              subscription.on("unsubscribed", (event) => {
                if (!disposed && event.code > 0 && event.code < 2500) {
                  status(LiveChannelConnectionState.Shutdown);
                  subscriber.error(
                    new Error(event.reason || "Live stream ended"),
                  );
                }
              });
              subscription.subscribe();
            } catch (error) {
              if (!disposed) subscriber.error(error);
            }
            return () => {
              disposed = true;
              if (subscription) {
                subscription.unsubscribe();
                c?.removeSubscription(subscription);
              }
              if (entries.get(id) === created) entries.delete(id);
              if (entries.size === 0) {
                client?.disconnect();
                client = undefined;
                connections.next(false);
              }
            };
          }).pipe(shareReplay({ bufferSize: 1, refCount: true })),
        };
        entry = created;
        entries.set(id, created);
      }
      const listener = entry.observable.subscribe({
        next: (event) => observer.next(event as LiveChannelEvent<T>),
        error: (error) => observer.error(error),
        complete: () => observer.complete(),
      });
      return () => listener.unsubscribe();
    });
  }
  return {
    getConnectionState: () => connections.asObservable(),
    getStream,
    getDataStream: (options: LiveDataStreamOptions) =>
      new Observable((subscriber) => {
        const buffer = {
          ...options.buffer,
          maxLength: Math.min(
            10000,
            Math.max(1, options.buffer?.maxLength || 500),
          ),
        };
        const frame = options.frame
          ? StreamingDataFrame.fromDataFrameJSON(options.frame, buffer)
          : StreamingDataFrame.empty(buffer);
        const refId = options.frame?.schema?.refId;
        const emit = () =>
          subscriber.next({
            data: [
              options.filter?.fields?.length
                ? {
                    ...frame,
                    length: frame.length,
                    fields: frame.fields.filter((field) =>
                      options.filter!.fields!.includes(field.name),
                    ),
                  }
                : frame,
            ],
            state: LoadingState.Streaming,
          });
        if (frame.length) {
          frame.refId = refId;
          emit();
        }
        const listener = getStream(options.addr)
          .pipe(
            filter(
              (event) =>
                event.type === LiveChannelEventType.Message ||
                (event.type === LiveChannelEventType.Status &&
                  event.message != null),
            ),
            map((event) =>
              event.type === LiveChannelEventType.Message
                ? event.message
                : event.type === LiveChannelEventType.Status
                  ? event.message
                  : undefined,
            ),
          )
          .subscribe({
            next: (value) => {
              try {
                if (!value || typeof value !== "object")
                  throw new Error(
                    "Live data stream requires a DataFrame JSON packet",
                  );
                frame.push(value as DataFrameJSON);
                frame.refId = refId;
                emit();
              } catch (error) {
                subscriber.error(error);
              }
            },
            error: (error) => subscriber.error(error),
            complete: () => subscriber.complete(),
          });
        return () => listener.unsubscribe();
      }),
    getPresence: async (address) => {
      const id = toLiveChannelId(address),
        entry = entries.get(id);
      if (!entry?.subscription)
        throw new Error("Subscribe before requesting channel presence");
      const result = await entry.subscription.presence();
      return { users: result.clients };
    },
    publish: async (address, data, options) => {
      if (options?.useSocket) {
        const c = connection();
        try {
          return await c.publish(toLiveChannelId(address), data);
        } finally {
          if (entries.size === 0 && client === c) {
            c.disconnect();
            client = undefined;
          }
        }
      }
      return api("/api/live/publish", {
        method: "POST",
        body: JSON.stringify({ channel: toLiveChannelId(address), data }),
      });
    },
  };
}
