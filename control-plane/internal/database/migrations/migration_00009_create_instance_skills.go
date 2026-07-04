package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
	"gorm.io/gorm"

	"github.com/gluk-w/claworc/control-plane/internal/database/models"
)

func init() {
	register(&goose.Migration{
		Version: 9,
		Source:  "00009_create_instance_skills.go",
		UpFnContext: func(ctx context.Context, tx *sql.Tx) error {
			return WithMigrator(ctx, tx, func(m gorm.Migrator, _ *gorm.DB) error {
				if !m.HasTable(&models.InstanceSkill{}) {
					// CreateTable automatically configures the ON DELETE CASCADE constraint on the database
					// based on the constraint:OnDelete:CASCADE tag on the Instance relation.
					if err := m.CreateTable(&models.InstanceSkill{}); err != nil {
						return err
					}
				}
				if !m.HasIndex(&models.InstanceSkill{}, "idx_instance_skill_slug") {
					if err := m.CreateIndex(&models.InstanceSkill{}, "idx_instance_skill_slug"); err != nil {
						return err
					}
				}
				return nil
			})
		},
		DownFnContext: func(ctx context.Context, tx *sql.Tx) error {
			return WithMigrator(ctx, tx, func(m gorm.Migrator, _ *gorm.DB) error {
				if m.HasTable(&models.InstanceSkill{}) {
					if err := m.DropTable(&models.InstanceSkill{}); err != nil {
						return err
					}
				}
				return nil
			})
		},
	})
}
