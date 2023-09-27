package logging

import (
	"context"
	"os"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type cachedTLData struct {
	Level   string `json:"level"`
	Expires int64  // Seconds since epoch
}

func SetAndStoreTimedLevel(ctx context.Context, tl log.TimedLevel, level string, duration time.Duration, procName string) error {
	tl.Set(ctx, level, duration)
	cd := cachedTLData{Level: level}
	if duration > 0 {
		cd.Expires = time.Now().Add(duration).Unix()
	}
	return cache.SaveToUserCache(ctx, &cd, procName+".loglevel", cache.Public)
}

func LoadTimedLevelFromCache(ctx context.Context, tl log.TimedLevel, procName string) error {
	file := procName + ".loglevel"
	cd := cachedTLData{}
	if err := cache.LoadFromUserCache(ctx, &cd, file); err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return err
	}
	if cd.Expires == 0 {
		tl.Set(ctx, cd.Level, 0)
	} else if duration := time.Until(time.Unix(cd.Expires, 0)); duration > 0 {
		tl.Set(ctx, cd.Level, duration)
	} else {
		// Time has expired, just drop the cache.
		_ = cache.DeleteFromUserCache(ctx, file)
	}
	return nil
}
