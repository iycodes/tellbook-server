package storage

import "testing"

func TestObjectCacheControlIsImmutableOnlyForPublicMedia(t *testing.T) {
	service := &R2Service{privateBucketName: "private", publicBucketName: "public"}

	if got := service.objectCacheControl("public"); got != publicObjectCacheControl {
		t.Fatalf("public cache control = %q", got)
	}
	if got := service.objectCacheControl("private"); got != "" {
		t.Fatalf("private cache control = %q, want empty", got)
	}
	if pointer := cacheControlPointer(""); pointer != nil {
		t.Fatal("empty cache control produced a metadata pointer")
	}
}
