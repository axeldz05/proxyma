package storage

// Bolt bucket names (SSOT). Add new buckets to allBuckets as well.
const (
	bucketSubscriptions              = "subscriptions"
	bucketServiceSubs                = "service_subscriptions"
	bucketNotifyOutbox             = "notify_outbox"
	bucketNotifyOutboxGenerations  = "notify_outbox_generations"
	bucketNotifyOutboxReservations = "notify_outbox_reservations"
	bucketDownloadIntents            = "download_intents"
	bucketPeers                      = "peers"
	bucketPendingInvites             = "pending_invites"
	bucketPipelineSchemas            = "pipeline_schemas"
	bucketPendingBlobGC              = "pending_blob_gc"
	vfsIndexBucket                   = "vfs_index"
	bucketTasksJournal               = "tasks_journal"
	bucketTasksActive                = "tasks_active"
	bucketTasksHistory               = "tasks_history"
)

var allBuckets = []string{
	bucketSubscriptions,
	bucketServiceSubs,
	bucketNotifyOutbox,
	bucketNotifyOutboxGenerations,
	bucketNotifyOutboxReservations,
	bucketDownloadIntents,
	bucketPeers,
	bucketPendingInvites,
	bucketPipelineSchemas,
	bucketPendingBlobGC,
	vfsIndexBucket,
	bucketTasksJournal,
	bucketTasksActive,
	bucketTasksHistory,
}
