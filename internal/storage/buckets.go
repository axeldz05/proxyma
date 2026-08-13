package storage

// Bolt bucket names (SSOT). Add new buckets to allBuckets as well.
const (
	bucketSubscriptions              = "subscriptions"
	bucketServiceSubs                = "service_subscriptions"
	bucketNotifyOutbox               = "notify_outbox" // legacy rows; reconciled by server before replay
	bucketNotifyOutboxV2             = "notify_outbox_v2"
	bucketNotifyOutboxV2Generations  = "notify_outbox_v2_generations"
	bucketNotifyOutboxV2Reservations = "notify_outbox_v2_reservations"
	bucketDownloadIntents            = "download_intents"
	bucketPeers                      = "peers"
	bucketPendingInvites             = "pending_invites"
	bucketPipelineSchemas            = "pipeline_schemas"
	bucketPendingBlobGC              = "pending_blob_gc"
	vfsIndexBucket                   = "vfs_index"
)

var allBuckets = []string{
	bucketSubscriptions,
	bucketServiceSubs,
	bucketNotifyOutbox,
	bucketNotifyOutboxV2,
	bucketNotifyOutboxV2Generations,
	bucketNotifyOutboxV2Reservations,
	bucketDownloadIntents,
	bucketPeers,
	bucketPendingInvites,
	bucketPipelineSchemas,
	bucketPendingBlobGC,
	vfsIndexBucket,
}
