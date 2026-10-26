// The kinds of footage an uploader can file a video under; each is a key
// prefix in the bucket.
export const STREAMS = ["drone", "headcam", "car", "other"] as const;
export type Stream = (typeof STREAMS)[number];
