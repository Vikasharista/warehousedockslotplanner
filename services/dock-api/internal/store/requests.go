package store

import (
	"context"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
)

// MaxTrucksPerRequest bounds one "Add trucks" request.
const MaxTrucksPerRequest = 50

// RequestJob is a queued request for generated trucks; the jobs' live loop fulfils it.
type RequestJob struct {
	JobID       int64      `json:"jobId"`
	FacilityID  string     `json:"facilityId"`
	Trucks      int        `json:"trucks"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requestedAt"`
	ProcessedAt *time.Time `json:"processedAt,omitempty"`
	Result      *string    `json:"result,omitempty"`
}

// QueueTrucks asks for n generated truck requests at the facility.
func (s *Store) QueueTrucks(ctx context.Context, facilityID string, n int) (*RequestJob, error) {
	if n < 1 || n > MaxTrucksPerRequest {
		return nil, errors.NewBadRequestError("trucks must be between 1 and 50")
	}
	var job RequestJob
	err := s.GetDB(ctx).Raw(`INSERT INTO truck_request_jobs (facility_id, trucks) VALUES (?, ?)
		RETURNING job_id, facility_id, trucks, status, requested_at, processed_at, result`, facilityID, n).
		Scan(&job).Error
	if err != nil {
		return nil, errors.RepositoryError(err)
	}
	return &job, nil
}

// RequestJob returns one queued request.
func (s *Store) RequestJob(ctx context.Context, id int64) (*RequestJob, error) {
	var jobs []RequestJob
	if err := s.GetDB(ctx).Raw(`SELECT job_id, facility_id, trucks, status, requested_at, processed_at, result
		FROM truck_request_jobs WHERE job_id = ?`, id).Scan(&jobs).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(jobs) == 0 {
		return nil, errors.NewRecordNotFoundError("no truck request job")
	}
	return &jobs[0], nil
}
