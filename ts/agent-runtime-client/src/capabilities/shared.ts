import type { RequestOptions } from "../runtime-client.js";

export type CapabilityRequest = <T>(path: string, init?: RequestInit, request?: RequestOptions) => Promise<T>;

export const pathPart = (value: string): string => encodeURIComponent(value);
