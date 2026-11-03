package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/submission"
	"github.com/google/uuid"
)

func (d *DB) SubmissionProfiles(ctx context.Context) ([]submission.Profile, error) {
	rows, err := d.QueryContext(ctx, `SELECT payload FROM submission_profiles ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []submission.Profile{}
	for rows.Next() {
		var b []byte
		var p submission.Profile
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) SaveSubmissionProfile(ctx context.Context, p submission.Profile) (submission.Profile, error) {
	if err := submission.ValidateProfile(p); err != nil {
		return p, err
	}
	expected := p.Revision
	p.UpdatedAt = time.Now().UTC()
	p.Revision++
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Fields == nil {
		p.Fields = []submission.Field{}
	}
	for i := range p.Fields {
		p.Fields[i].Value = ""
		p.Fields[i].Sources = []string{}
	}
	b, _ := json.Marshal(p)
	var res sql.Result
	var err error
	if expected == 0 {
		res, err = d.ExecContext(ctx, `INSERT INTO submission_profiles(id,revision,payload) VALUES($1,1,$2) ON CONFLICT DO NOTHING`, p.ID, b)
	} else {
		res, err = d.ExecContext(ctx, `UPDATE submission_profiles SET revision=$2,payload=$3,updated_at=now() WHERE id=$1 AND revision=$4`, p.ID, p.Revision, b, expected)
	}
	if err != nil {
		return p, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return p, submission.ErrConflict
	}
	return p, nil
}

type submissionQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func submissionProfile(ctx context.Context, q submissionQuery, id string, lock bool) (submission.Profile, error) {
	query := `SELECT payload FROM submission_profiles WHERE id=$1`
	if lock {
		query += ` FOR SHARE`
	}
	var b []byte
	var p submission.Profile
	err := q.QueryRowContext(ctx, query, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	return p, err
}
func submissionSource(ctx context.Context, q submissionQuery, id string, lock bool) (submission.Source, error) {
	query := `SELECT id,COALESCE(name,''),vulnclass,severity,status,summary,evidence,COALESCE(report,''),asset_ids::text,evidence_version,report_evidence_version FROM findings WHERE id=$1`
	if lock {
		query += ` FOR SHARE`
	}
	var s submission.Source
	var fid int64
	err := q.QueryRowContext(ctx, query, id).Scan(&fid, &s.Name, &s.VulnClass, &s.Severity, &s.Status, &s.Summary, &s.Evidence, &s.Report, &s.AssetIDs, &s.EvidenceVersion, &s.ReportEvidenceVersion)
	s.FindingID = strconv.FormatInt(fid, 10)
	return s, err
}
func submissionDraft(ctx context.Context, q submissionQuery, id string, lock bool) (submission.Draft, error) {
	query := `SELECT payload FROM submission_drafts WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var b []byte
	var draft submission.Draft
	err := q.QueryRowContext(ctx, query, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &draft)
	}
	return draft, err
}

func (d *DB) PrepareSubmission(ctx context.Context, fid, pid string) (submission.Draft, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return submission.Draft{}, err
	}
	defer tx.Rollback()
	p, err := submissionProfile(ctx, tx, pid, true)
	if err != nil {
		return submission.Draft{}, err
	}
	src, err := submissionSource(ctx, tx, fid, true)
	if err != nil {
		return submission.Draft{}, err
	}
	draft := submission.NewDraft(src, p)
	draft.ID = uuid.NewString()
	draft.Revision = 1
	draft.UpdatedAt = time.Now().UTC()
	b, _ := json.Marshal(draft)
	_, err = tx.ExecContext(ctx, `INSERT INTO submission_drafts(id,finding_id,profile_id,revision,payload) VALUES($1,$2,$3,1,$4) ON CONFLICT(finding_id,profile_id) DO NOTHING`, draft.ID, fid, pid, b)
	if err != nil {
		return draft, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM submission_drafts WHERE finding_id=$1 AND profile_id=$2`, fid, pid).Scan(&b); err != nil {
		return draft, err
	}
	if err = json.Unmarshal(b, &draft); err != nil {
		return draft, err
	}
	return draft, tx.Commit()
}

func (d *DB) SubmissionView(ctx context.Context, id string) (submission.View, error) {
	draft, err := submissionDraft(ctx, d, id, false)
	if err != nil {
		return submission.View{}, err
	}
	src, err := submissionSource(ctx, d, draft.FindingID, false)
	if err != nil {
		return submission.View{}, err
	}
	p, err := submissionProfile(ctx, d, draft.ProfileID, false)
	if err != nil {
		return submission.View{}, err
	}
	return submission.View{Draft: draft, Checks: submission.Checks(draft, src, p)}, nil
}

// EditSubmission atomically binds edits/confirmation to a draft revision and the
// current finding and program. Model calls must happen before entering this lock.
func (d *DB) EditSubmission(ctx context.Context, id string, revision int64, edit func(*submission.Draft, submission.Source, submission.Profile) error) (submission.View, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return submission.View{}, err
	}
	defer tx.Rollback()
	draft, err := submissionDraft(ctx, tx, id, true)
	if err != nil {
		return submission.View{}, err
	}
	if draft.Revision != revision {
		return submission.View{}, submission.ErrConflict
	}
	p, err := submissionProfile(ctx, tx, draft.ProfileID, true)
	if err != nil {
		return submission.View{}, err
	}
	src, err := submissionSource(ctx, tx, draft.FindingID, true)
	if err != nil {
		return submission.View{}, err
	}
	if err = edit(&draft, src, p); err != nil {
		return submission.View{}, err
	}
	if err = submission.ValidateContent(draft); err != nil {
		return submission.View{}, err
	}
	draft.Revision++
	draft.UpdatedAt = time.Now().UTC()
	b, _ := json.Marshal(draft)
	_, err = tx.ExecContext(ctx, `UPDATE submission_drafts SET revision=$2,payload=$3,updated_at=now() WHERE id=$1`, id, draft.Revision, b)
	if err != nil {
		return submission.View{}, err
	}
	if err = tx.Commit(); err != nil {
		return submission.View{}, err
	}
	return submission.View{Draft: draft, Checks: submission.Checks(draft, src, p)}, nil
}

func (d *DB) FreshSubmissionBase(ctx context.Context, id string) (submission.Draft, error) {
	old, err := submissionDraft(ctx, d, id, false)
	if err != nil {
		return old, err
	}
	p, err := submissionProfile(ctx, d, old.ProfileID, false)
	if err != nil {
		return old, err
	}
	src, err := submissionSource(ctx, d, old.FindingID, false)
	if err != nil {
		return old, err
	}
	base := submission.NewDraft(src, p)
	base.ID = old.ID
	base.Revision = old.Revision
	return base, nil
}

func SubmissionNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
