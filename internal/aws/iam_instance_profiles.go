package aws

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// IAMInstanceProfile is a root-path native IAM container, not an EC2 runtime or
// credential authority. Names survive deletion; every recreation gets a new ID.
type IAMInstanceProfile struct {
	Path                string
	InstanceProfileName string
	InstanceProfileID   string `xml:"InstanceProfileId"`
	Arn                 string
	CreateDate          time.Time
	Roles               []IAMRole `xml:"Roles>member"`
}

var iamInstanceProfileID = regexp.MustCompile(`^AIPA[A-F0-9]{16}$`)

const iamInstanceProfileARN = "arn:aws:iam::000000000000:instance-profile/"

func iamInstanceProfileSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_iam_instance_profiles (
 name TEXT PRIMARY KEY COLLATE NOCASE, profile_id TEXT NOT NULL,
 created TEXT NOT NULL, deleted INTEGER NOT NULL CHECK(deleted IN(0,1)),
 UNIQUE(name,profile_id));
 CREATE TABLE IF NOT EXISTS aws_iam_instance_profile_roles (
 profile_name TEXT PRIMARY KEY COLLATE NOCASE, profile_id TEXT NOT NULL,
 role_name TEXT NOT NULL COLLATE NOCASE, role_id TEXT NOT NULL,
 FOREIGN KEY(profile_name,profile_id) REFERENCES aws_iam_instance_profiles(name,profile_id),
 FOREIGN KEY(role_name,role_id) REFERENCES aws_iam_roles(name,role_id));`)
	return err
}
func iamProfileBad() error { return fmt.Errorf("invalid persisted local IAM instance profile state") }
func iamProfileRead(ctx context.Context, tx *sql.Tx, name string, retired bool) (IAMInstanceProfile, error) {
	var p IAMInstanceProfile
	var created string
	var deleted int
	err := tx.QueryRowContext(ctx, `SELECT name,profile_id,created,deleted FROM aws_iam_instance_profiles WHERE name=?`, name).Scan(&p.InstanceProfileName, &p.InstanceProfileID, &created, &deleted)
	if err == sql.ErrNoRows {
		return p, ssmFailure{"NoSuchEntity", "The instance profile does not exist."}
	}
	if err != nil {
		return p, err
	}
	if !iamInlineName.MatchString(p.InstanceProfileName) || !iamInstanceProfileID.MatchString(p.InstanceProfileID) || (deleted != 0 && deleted != 1) {
		return p, iamProfileBad()
	}
	p.CreateDate, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return p, iamProfileBad()
	}
	p.Path = "/"
	p.Arn = iamInstanceProfileARN + p.InstanceProfileName
	p.Roles = []IAMRole{}
	var pn, pid, rn, rid string
	err = tx.QueryRowContext(ctx, `SELECT profile_name,profile_id,role_name,role_id FROM aws_iam_instance_profile_roles WHERE profile_name=?`, p.InstanceProfileName).Scan(&pn, &pid, &rn, &rid)
	if err != nil && err != sql.ErrNoRows {
		return p, err
	}
	if err == nil {
		if deleted != 0 || pn != p.InstanceProfileName || pid != p.InstanceProfileID {
			return p, iamProfileBad()
		}
		role, e := iamProfileRole(ctx, tx, rn)
		if e != nil {
			return p, iamProfileBad()
		}
		if role.RoleName != rn || role.RoleID != rid || rid == "" {
			return p, iamProfileBad()
		}
		p.Roles = append(p.Roles, role)
	}
	if deleted != 0 && !retired {
		return p, ssmFailure{"NoSuchEntity", "The instance profile does not exist."}
	}
	return p, nil
}

// Membership never adopts legacy aggregates or malformed persisted native roles.
func iamProfileRole(ctx context.Context, tx *sql.Tx, name string) (IAMRole, error) {
	role, err := iamManagedRole(ctx, tx, name)
	if err != nil {
		return role, err
	}
	if _, err = iamTrust(role.AssumeRolePolicyDocument); err != nil {
		return role, iamProfileBad()
	}
	if role.MaxSessionDuration < 3600 || role.MaxSessionDuration > 43200 {
		return role, iamProfileBad()
	}
	return role, nil
}
func iamProfileExpected(p IAMInstanceProfile, expected string) error {
	if expected == "" {
		return ssmFailure{"InvalidInput", "An admitted instance profile incarnation is required."}
	}
	if p.InstanceProfileID != expected {
		return ssmFailure{"NoSuchEntity", "The admitted instance profile incarnation no longer exists."}
	}
	return nil
}
func (s *IAMRoleStore) GetInstanceProfile(ctx context.Context, name string, expected ...string) (IAMInstanceProfile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	defer tx.Rollback()
	p, err := iamProfileRead(ctx, tx, name, false)
	if err == nil && len(expected) > 0 {
		if len(expected) != 1 {
			err = iamProfileBad()
		} else {
			err = iamProfileExpected(p, expected[0])
		}
	}
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	return p, tx.Commit()
}
func (s *IAMRoleStore) CreateInstanceProfile(ctx context.Context, name string) (IAMInstanceProfile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	defer tx.Rollback()
	value, err := iamCreateInstanceProfileTx(ctx, tx, name, "")
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	return value, tx.Commit()
}
func iamCreateInstanceProfileTx(ctx context.Context, tx *sql.Tx, name, stackID string) (IAMInstanceProfile, error) {
	if !iamInlineName.MatchString(name) {
		return IAMInstanceProfile{}, ssmFailure{"ValidationError", "Use a root-path instance profile name of 1–128 IAM name characters."}
	}
	id, err := s3NewID()
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	id = "AIPA" + strings.ToUpper(id[:16])
	if err = iamStackNameAvailable(ctx, tx, "AWS::IAM::InstanceProfile", name, stackID); err != nil {
		return IAMInstanceProfile{}, err
	}
	var count, active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_iam_instance_profiles`).Scan(&count); err != nil {
		return IAMInstanceProfile{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_iam_instance_profiles WHERE name=? AND deleted=0`, name).Scan(&active); err != nil {
		return IAMInstanceProfile{}, err
	}
	if active != 0 {
		return IAMInstanceProfile{}, ssmFailure{"EntityAlreadyExists", "The instance profile already exists."}
	}
	old, e := iamProfileRead(ctx, tx, name, true)
	if e != nil && iamProfileErrorCode(e) != "NoSuchEntity" {
		return IAMInstanceProfile{}, e
	}
	if e != nil && count >= 1000 {
		return IAMInstanceProfile{}, ssmFailure{"LimitExceeded", "Local active and retired instance profile names are limited to 1000."}
	}
	if e == nil && len(old.Roles) != 0 {
		return IAMInstanceProfile{}, iamProfileBad()
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	err = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_instance_profiles(name,profile_id,created,deleted) VALUES(?,?,?,0)
 ON CONFLICT(name) DO UPDATE SET name=excluded.name,profile_id=excluded.profile_id,created=excluded.created,deleted=0 WHERE aws_iam_instance_profiles.deleted=1`, name, id, created))
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	p, err := iamProfileRead(ctx, tx, name, false)
	if err != nil {
		return IAMInstanceProfile{}, err
	}
	if p.InstanceProfileID != id || p.InstanceProfileName != name || p.CreateDate.Format(time.RFC3339Nano) != created || len(p.Roles) != 0 {
		return IAMInstanceProfile{}, iamProfileBad()
	}
	return p, nil
}
func iamProfileErrorCode(err error) string {
	if e, ok := err.(ssmFailure); ok {
		return e.code
	}
	return ""
}

// Mutations require the exact identities admitted before authorization. The
// generation checks, membership changes and postcondition checks share one tx.
func (s *IAMRoleStore) DeleteInstanceProfile(ctx context.Context, name, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = iamDeleteInstanceProfileTx(ctx, tx, name, id); err != nil {
		return err
	}
	return tx.Commit()
}
func iamDeleteInstanceProfileTx(ctx context.Context, tx *sql.Tx, name, id string) error {
	p, err := iamProfileRead(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if err = iamProfileExpected(p, id); err != nil {
		return err
	}
	if len(p.Roles) != 0 {
		return ssmFailure{"DeleteConflict", "Remove the role before deleting the instance profile."}
	}
	if err = iamExactlyOne(tx.ExecContext(ctx, `UPDATE aws_iam_instance_profiles SET deleted=1 WHERE name=? AND profile_id=? AND deleted=0`, p.InstanceProfileName, id)); err != nil {
		return err
	}
	retired, err := iamProfileRead(ctx, tx, name, true)
	if err != nil {
		return err
	}
	var deleted int
	if err = tx.QueryRowContext(ctx, `SELECT deleted FROM aws_iam_instance_profiles WHERE name=? AND profile_id=?`, name, id).Scan(&deleted); err != nil {
		return err
	}
	if deleted != 1 || retired.InstanceProfileID != id || retired.InstanceProfileName != p.InstanceProfileName || !retired.CreateDate.Equal(p.CreateDate) {
		return iamProfileBad()
	}
	return nil
}
func (s *IAMRoleStore) ChangeInstanceProfileRole(ctx context.Context, name, id, roleName, roleID string, add bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = iamChangeInstanceProfileRoleTx(ctx, tx, name, id, roleName, roleID, add); err != nil {
		return err
	}
	return tx.Commit()
}
func iamChangeInstanceProfileRoleTx(ctx context.Context, tx *sql.Tx, name, id, roleName, roleID string, add bool) error {
	p, err := iamProfileRead(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if err = iamProfileExpected(p, id); err != nil {
		return err
	}
	role, err := iamProfileRole(ctx, tx, roleName)
	if err != nil {
		return err
	}
	if err = iamManagedExpectedRole(role, []string{roleID}); err != nil {
		return err
	}
	if add {
		if len(p.Roles) != 0 {
			if p.Roles[0].RoleID == role.RoleID {
				return ssmFailure{"EntityAlreadyExists", "The role is already in the instance profile."}
			}
			return ssmFailure{"LimitExceeded", "An instance profile can contain only one role."}
		}
		err = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_instance_profile_roles(profile_name,profile_id,role_name,role_id) VALUES(?,?,?,?)`, p.InstanceProfileName, id, role.RoleName, roleID))
	} else {
		if len(p.Roles) != 1 || p.Roles[0].RoleID != role.RoleID {
			return ssmFailure{"NoSuchEntity", "The role is not in this instance profile."}
		}
		err = iamExactlyOne(tx.ExecContext(ctx, `DELETE FROM aws_iam_instance_profile_roles WHERE profile_name=? AND profile_id=? AND role_name=? AND role_id=?`, p.InstanceProfileName, id, role.RoleName, roleID))
	}
	if err != nil {
		return err
	}
	after, err := iamProfileRead(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if after.InstanceProfileID != id || after.InstanceProfileName != p.InstanceProfileName || !after.CreateDate.Equal(p.CreateDate) || add && (len(after.Roles) != 1 || after.Roles[0] != role) || !add && len(after.Roles) != 0 {
		return iamProfileBad()
	}
	return nil
}
func (s *IAMRoleStore) ListInstanceProfiles(ctx context.Context, after string, limit int, roleName, roleID string) ([]IAMInstanceProfile, error) {
	if limit < 1 || limit > 1001 || after != "" && !iamInlineName.MatchString(after) {
		return nil, ssmFailure{"InvalidInput", "Invalid instance profile pagination."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if roleName != "" {
		role, e := iamProfileRole(ctx, tx, roleName)
		if e != nil {
			return nil, e
		}
		if e = iamManagedExpectedRole(role, []string{roleID}); e != nil {
			return nil, e
		}
		roleName = role.RoleName
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM aws_iam_instance_profiles WHERE deleted=0 AND name>? COLLATE NOCASE AND (?='' OR EXISTS(SELECT 1 FROM aws_iam_instance_profile_roles m WHERE m.profile_name=aws_iam_instance_profiles.name AND m.role_name=?)) ORDER BY name COLLATE NOCASE LIMIT ?`, after, roleName, roleName, limit)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	profiles := []IAMInstanceProfile{}
	for _, name := range names {
		p, e := iamProfileRead(ctx, tx, name, false)
		if e != nil {
			return nil, e
		}
		profiles = append(profiles, p)
	}
	return profiles, tx.Commit()
}
