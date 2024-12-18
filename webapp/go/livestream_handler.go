package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type LivestreamViewerModel struct {
	UserID       int64 `db:"user_id" json:"user_id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	CreatedAt    int64 `db:"created_at" json:"created_at"`
}

type LivestreamModel struct {
	ID             int64  `db:"id" json:"id"`
	UserID         int64  `db:"user_id" json:"user_id"`
	Title          string `db:"title" json:"title"`
	Description    string `db:"description" json:"description"`
	PlaylistUrl    string `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl   string `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt        int64  `db:"start_at" json:"start_at"`
	EndAt          int64  `db:"end_at" json:"end_at"`
	Tip            int64  `db:"tip"`
	ReactionsCount int64  `db:"reactions_count"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type LivestreamTagModel struct {
	ID           int64 `db:"id" json:"id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	TagID        int64 `db:"tag_id" json:"tag_id"`
}

type ReservationSlotModel struct {
	ID      int64 `db:"id" json:"id"`
	Slot    int64 `db:"slot" json:"slot"`
	StartAt int64 `db:"start_at" json:"start_at"`
	EndAt   int64 `db:"end_at" json:"end_at"`
}

type IconModel struct {
	ID       int64  `db:"id" json:"id"`
	UserID   int64  `db:"user_id" json:"user_id"`
	IconHash string `db:"icon_hash" json:"icon_hash"`
}

func reserveLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ReserveLivestreamRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// 2023/11/25 10:00からの１年間の期間内であるかチェック
	var (
		termStartAt    = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
		termEndAt      = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
		reserveStartAt = time.Unix(req.StartAt, 0)
		reserveEndAt   = time.Unix(req.EndAt, 0)
	)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	// 予約枠をみて、予約が可能か調べる
	// NOTE: 並列な予約のoverbooking防止にFOR UPDATEが必要
	var slots []*ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots WHERE start_at >= ? AND end_at <= ? FOR UPDATE", req.StartAt, req.EndAt); err != nil {
		c.Logger().Warnf("予約枠一覧取得でエラー発生: %+v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
	}
	for _, slot := range slots {
		var count int
		if err := tx.GetContext(ctx, &count, "SELECT slot FROM reservation_slots WHERE start_at = ? AND end_at = ?", slot.StartAt, slot.EndAt); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
		}
		c.Logger().Infof("%d ~ %d予約枠の残数 = %d\n", slot.StartAt, slot.EndAt, slot.Slot)
		if count < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
	}

	var (
		livestreamModel = &LivestreamModel{
			UserID:       int64(userID),
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		}
	)

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update reservation_slot: "+err.Error())
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(:user_id, :title, :description, :playlist_url, :thumbnail_url, :start_at, :end_at)", livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream: "+err.Error())
	}

	livestreamID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livestream id: "+err.Error())
	}
	livestreamModel.ID = livestreamID

	// タグ追加
	for _, tagID := range req.Tags {
		if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (:livestream_id, :tag_id)", &LivestreamTagModel{
			LivestreamID: livestreamID,
			TagID:        tagID,
		}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream tag: "+err.Error())
		}
	}

	livestream, err := fillLivestreamResponse(ctx, tx, *livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livestream)
}

func searchLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	keyTagName := c.QueryParam("tag")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModels []*LivestreamModel
	if c.QueryParam("tag") != "" {
		// タグによる取得
		var tagID int
		if err := tx.GetContext(ctx, &tagID, "SELECT id FROM tags WHERE name = ?", keyTagName); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tag: "+err.Error())
		}
		var keyTaggedLivestreamIDs []int64
		if err := tx.SelectContext(ctx, &keyTaggedLivestreamIDs, "SELECT livestream_id FROM livestream_tags WHERE tag_id = ? ORDER BY livestream_id DESC", tagID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get keyTaggedLivestreamIDs: "+err.Error())
		}

		if len(keyTaggedLivestreamIDs) > 0 {
			query, params, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?) ORDER BY id DESC", keyTaggedLivestreamIDs)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query: "+err.Error())
			}
			if err := tx.SelectContext(ctx, &livestreamModels, query, params...); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
			}
		}

	} else {
		// 検索条件なし
		query := `SELECT * FROM livestreams ORDER BY id DESC`
		if c.QueryParam("limit") != "" {
			limit, err := strconv.Atoi(c.QueryParam("limit"))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			query += fmt.Sprintf(" LIMIT %d", limit)
		}

		if err := tx.SelectContext(ctx, &livestreamModels, query); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
		}
	}

	livestreamIDs := make([]int64, len(livestreamModels))
	for i, livestreamModel := range livestreamModels {
		livestreamIDs[i] = livestreamModel.ID
	}

	// Fetch all tags for the livestreams in one query
	var livestreamTags []LivestreamTagModel
	query, args, err := sqlx.In("SELECT * FROM livestream_tags WHERE livestream_id IN (?)", livestreamIDs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query for tags: "+err.Error())
	}
	query = tx.Rebind(query)
	if err := tx.SelectContext(ctx, &livestreamTags, query, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream tags: "+err.Error())
	}

	// Group tags by livestream ID
	tagsByLivestreamID := make(map[int64][]int64)
	for _, tag := range livestreamTags {
		tagsByLivestreamID[tag.LivestreamID] = append(tagsByLivestreamID[tag.LivestreamID], tag.TagID)
	}

	// Fetch all tags in one query
	var tagIDs []int64
	for _, tagIDList := range tagsByLivestreamID {
		tagIDs = append(tagIDs, tagIDList...)
	}
	var tags []Tag
	if len(tagIDs) > 0 {
		query, args, err := sqlx.In("SELECT * FROM tags WHERE id IN (?)", tagIDs)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query for tags: "+err.Error())
		}
		query = tx.Rebind(query)
		if err := tx.SelectContext(ctx, &tags, query, args...); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
		}
	}

	// Map tags by ID for quick lookup
	tagsByID := make(map[int64]Tag)
	for _, tag := range tags {
		tagsByID[tag.ID] = tag
	}

	// Fetch all users in one query
	var userIDs []int64
	for _, livestreamModel := range livestreamModels {
		userIDs = append(userIDs, livestreamModel.UserID)
	}
	var users []UserModel
	query, args, err = sqlx.In("SELECT * FROM users WHERE id IN (?)", userIDs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query for users: "+err.Error())
	}
	query = tx.Rebind(query)
	if err := tx.SelectContext(ctx, &users, query, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get users: "+err.Error())
	}

	// Map users by ID for quick lookup
	usersByID := make(map[int64]UserModel)
	for _, user := range users {
		usersByID[user.ID] = user
	}

	// Fetch all themes for the users in one query
	var themes []ThemeModel
	query, args, err = sqlx.In("SELECT * FROM themes WHERE user_id IN (?)", userIDs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query for themes: "+err.Error())
	}
	query = tx.Rebind(query)
	if err := tx.SelectContext(ctx, &themes, query, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get themes: "+err.Error())
	}

	// Map themes by user ID for quick lookup
	themesByUserID := make(map[int64][]ThemeModel)
	for _, theme := range themes {
		themesByUserID[theme.UserID] = append(themesByUserID[theme.UserID], theme)
	}

	// Fetch all icons for the users in one query
	var icons []IconModel
	query, args, err = sqlx.In("SELECT id, user_id, icon_hash FROM icons WHERE user_id IN (?)", userIDs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query for icons: "+err.Error())
	}
	query = tx.Rebind(query)
	if err := tx.SelectContext(ctx, &icons, query, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get icons: "+err.Error())
	}

	// Map icons by user ID for quick lookup
	iconsByUserID := make(map[int64]IconModel)
	for _, icon := range icons {
		iconsByUserID[icon.UserID] = icon
	}

	owners := make(map[int64]User)
	for _, user := range users {
		theme := Theme{}
		if userThemes, ok := themesByUserID[user.ID]; ok && len(userThemes) > 0 {
			theme = Theme{
				ID:       userThemes[0].ID,
				DarkMode: userThemes[0].DarkMode,
			}
		}

		iconHash := "d9f8294e9d895f81ce62e73dc7d5dff862a4fa40bd4e0fecf53f7526a8edcac0"
		if icon, ok := iconsByUserID[user.ID]; ok {
			iconHash = icon.IconHash
		}

		owner := User{
			ID:          user.ID,
			Name:        user.Name,
			DisplayName: user.DisplayName,
			Description: user.Description,
			Theme:       theme,
			IconHash:    iconHash,
		}

		owners[user.ID] = owner
	}

	// Construct the response
	livestreams := make([]Livestream, len(livestreamModels))
	for i, livestreamModel := range livestreamModels {
		owner := owners[livestreamModel.UserID]

		tagIDs := tagsByLivestreamID[livestreamModel.ID]
		livestreamTags := make([]Tag, len(tagIDs))
		for j, tagID := range tagIDs {
			livestreamTags[j] = tagsByID[tagID]
		}

		livestreams[i] = Livestream{
			ID:           livestreamModel.ID,
			Owner:        owner,
			Title:        livestreamModel.Title,
			Description:  livestreamModel.Description,
			PlaylistUrl:  livestreamModel.PlaylistUrl,
			ThumbnailUrl: livestreamModel.ThumbnailUrl,
			Tags:         livestreamTags,
			StartAt:      livestreamModel.StartAt,
			EndAt:        livestreamModel.EndAt,
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams := make([]Livestream, len(livestreamModels))
	for i := range livestreamModels {
		livestream, err := fillLivestreamResponse(ctx, tx, *livestreamModels[i])
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
		}
		livestreams[i] = livestream
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams := make([]Livestream, len(livestreamModels))
	for i := range livestreamModels {
		livestream, err := fillLivestreamResponse(ctx, tx, *livestreamModels[i])
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
		}
		livestreams[i] = livestream
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

// viewerテーブルの廃止
func enterLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	livestreamModel := LivestreamModel{}
	err = tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	livestream, err := fillLivestreamResponse(ctx, tx, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestream)
}

func getLivecommentReportsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModel LivestreamModel
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	// error already check
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already check
	userID := sess.Values[defaultUserIDKey].(int64)

	if livestreamModel.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	var reportModels []*LivecommentReportModel
	if err := tx.SelectContext(ctx, &reportModels, "SELECT * FROM livecomment_reports WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment reports: "+err.Error())
	}

	reports := make([]LivecommentReport, len(reportModels))
	for i := range reportModels {
		report, err := fillLivecommentReportResponse(ctx, tx, *reportModels[i])
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
		}
		reports[i] = report
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, reports)
}

func fillLivestreamResponse(ctx context.Context, tx *sqlx.Tx, livestreamModel LivestreamModel) (Livestream, error) {
	ownerModel := UserModel{}
	if err := tx.GetContext(ctx, &ownerModel, "SELECT * FROM users WHERE id = ?", livestreamModel.UserID); err != nil {
		return Livestream{}, err
	}
	owner, err := fillUserResponse(ctx, tx, ownerModel)
	if err != nil {
		return Livestream{}, err
	}

	var tagIDs []int64
	if err := tx.SelectContext(ctx, &tagIDs, "SELECT tag_id FROM livestream_tags WHERE livestream_id = ?", livestreamModel.ID); err != nil {
		return Livestream{}, err
	}

	tags := make([]Tag, len(tagIDs))
	if len(tagIDs) > 0 {
		query, args, err := sqlx.In("SELECT * FROM tags WHERE id IN (?)", tagIDs)
		if err != nil {
			return Livestream{}, err
		}
		query = tx.Rebind(query)
		if err := tx.SelectContext(ctx, &tags, query, args...); err != nil {
			return Livestream{}, err
		}
	}

	livestream := Livestream{
		ID:           livestreamModel.ID,
		Owner:        owner,
		Title:        livestreamModel.Title,
		Tags:         tags,
		Description:  livestreamModel.Description,
		PlaylistUrl:  livestreamModel.PlaylistUrl,
		ThumbnailUrl: livestreamModel.ThumbnailUrl,
		StartAt:      livestreamModel.StartAt,
		EndAt:        livestreamModel.EndAt,
	}
	return livestream, nil
}
