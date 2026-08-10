package storage

// Bolt bucket names (SSOT). Add new buckets to allBuckets as well.
const (
	bucketSubscriptions   = "subscriptions"
	bucketServiceSubs     = "service_subscriptions"
	bucketNotifyOutbox    = "notify_outbox"
	bucketPeers           = "peers"
	bucketPipelineSchemas = "pipeline_schemas"
	vfsIndexBucket        = "vfs_index"
)

var allBuckets = []string{
	bucketSubscriptions,
	bucketServiceSubs,
	bucketNotifyOutbox,
	bucketPeers,
	bucketPipelineSchemas,
	vfsIndexBucket,
}
