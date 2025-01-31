package models

import (
	"context"
	"errors"
	"github.com/go-redsync/redsync"
	"github.com/jmoiron/sqlx"
	"time"
)

type CategoryDLRequest struct {
	Url     string
	Website string
	Id      string
	Db      *sqlx.DB
	Redsync *redsync.Redsync
}

//func NewVideoDlRequest(Url, id string, Db *sqlx.DB, redsync2 *redsync.Redsync) *CategoryDLRequest {
//	return &CategoryDLRequest{
//		Url: Url,
//		Id:      id,
//		Db:      Db,
//		Redsync: redsync2,
//	}
//}

// RefreshLock refreshes the lock for this download request, preventing it from being acquired by another scheduler.
func (v *CategoryDLRequest) RefreshLock() error {
	_, err := v.Db.Exec("UPDATE downloads SET lock = Now() WHERE id = $1", v.Id)
	return err
}

var NeverDownloaded = errors.New("no video for category")

//// Only relevant for tags
//func (v *CategoryDLRequest) GetLatestVideoForRequest() (*string, error) {
//	curs, err := v.Db.Query("SELECT videos.video_id from videos INNER JOIN downloads ON videos.download_id = downloads.id "+
//		"WHERE attribute_type=$1 AND attribute_value=$2 AND downloads.website=$3 AND videos.upload_time IS NOT NULL "+
//		"ORDER BY upload_time desc LIMIT 1",
//		v.ContentType, v.ContentValue, v.Website)
//	if err != nil {
//		return nil, err
//	}
//
//	var videoIDList []string
//	for curs.Next() {
//		var i string
//		err := curs.Scan(&i)
//		if err != nil {
//			return nil, err
//		}
//		videoIDList = append(videoIDList, i)
//	}
//
//	if len(videoIDList) == 0 {
//		return nil, NeverDownloaded
//	} else if len(videoIDList) != 1 {
//		return nil, fmt.Errorf("videoIDList had the wrong length. Length: %d", len(videoIDList))
//	}
//
//	return &videoIDList[0], nil
//}

const (
	MINIMUM_BACKOFF_TIME   = time.Hour * 24
	MAXIMUM_BACKOFF_FACTOR = 8 // 8 days
)

// IsBackingOff indicates whether the archive request is backing off from full content syncs
// Context: videos can be added to a category of content at any time; some categories are updated frequently, and some
// tend to be stagnant. We should vary the rate at which we fully sync content from a given category based on how
// frequently it's updated. Exponential backoff is used as the backoff strategy.
func (v *CategoryDLRequest) IsBackingOff() (bool, error) {
	var lastSynced time.Time
	var backoffFactor int
	sql := "SELECT last_synced, backoff_factor FROM downloads WHERE id = $1"

	rows := v.Db.QueryRow(sql, v.Id)
	err := rows.Scan(&lastSynced, &backoffFactor)
	if err != nil {
		return false, err
	}

	// I did what I had to do...
	return time.Now().Sub(lastSynced.Add(MINIMUM_BACKOFF_TIME*time.Duration(backoffFactor))) < 0, nil
}

func (v *CategoryDLRequest) ReportSyncHit() error {
	sql := "UPDATE downloads SET backoff_factor = 1, last_synced = Now() WHERE id = $1"
	_, err := v.Db.Exec(sql, v.Id)
	if err != nil {
		return err
	}

	return nil
}

func (v *CategoryDLRequest) ReportSyncMiss() error {
	// maybe there's an easier way to do this? It doesn't really matter though
	tx, err := v.Db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	var backoff_factor uint32
	row := tx.QueryRow("SELECT backoff_factor FROM downloads WHERE id = $1", v.Id)
	err = row.Scan(&backoff_factor)
	if err != nil {
		tx.Rollback()
		return err
	}

	sql := "UPDATE downloads SET backoff_factor = $1, last_synced = Now() WHERE id = $2"
	_, err = v.Db.Exec(sql, min(MAXIMUM_BACKOFF_FACTOR, backoff_factor*2), v.Id)
	if err != nil {
		tx.Rollback()
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	return nil
}

// Idempotent, ensures that videos are added and correct associations are created
// returns bool indicating whether something was added
func (v *CategoryDLRequest) AddVideo(videoID, url string) (bool, error) {
	tx, err := v.Db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, err
	}

	if url == "" {
		return false, errors.New("url cannot be blank")
	}

	if videoID == "" {
		return false, errors.New("video ID cannot be blank")
	}

	var id uint32
	sql := "INSERT INTO videos (video_ID, Url, website) VALUES ($1, $2, $3) " +
		"ON CONFLICT (video_ID, website) DO UPDATE set video_ID = EXCLUDED.video_ID RETURNING id"
	row := tx.QueryRow(sql, videoID, url, v.Website)
	err = row.Scan(&id)
	if err != nil {
		return false, err
	}

	sql = "INSERT INTO downloads_to_videos (download_id, video_id) VALUES ($1, $2) ON CONFLICT DO NOTHING"
	res, err := tx.Exec(sql, v.Id, id)
	if err != nil {
		tx.Rollback()
		return false, err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		tx.Rollback()
		return false, err
	}

	err = tx.Commit()
	if err != nil {
		tx.Rollback()
		return false, err
	}

	return rowsAffected >= 1, nil
}

func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
