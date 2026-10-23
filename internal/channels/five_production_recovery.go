package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// Recovery consumes the exact complete response sealed by the executor before
// parsing. It issues no HTTP. The response's full original immutable request
// must already match the journal request, including every preceding checkpoint.
func (p *PreparedProduction) recoverFiveResponse(ctx context.Context, m Materials, q protocol.Request, env fiveResponseEnvelope) (*protocol.Receipt, error) {
	if !env.Complete || env.HTTPStatus != 200 {
		return nil, nil
	}
	t := p.target
	o, parseErr := decode(env.Body)
	if parseErr != nil && !(q.Kind == "upload" && (q.Channel == "huawei" || q.Channel == "tencent")) {
		return nil, nil
	}
	var ref string
	var err error
	success := false
	switch q.Kind {
	case "authorize":
		var g UploadGrant
		switch q.Channel {
		case "huawei":
			g, err = parseHuaweiUploadGrant(o, t.APK, env.StartedAt)
		case "honor":
			g, err = parseHonorUploadGrant(o, t.AppID, t.APK, env.StartedAt)
		case "tencent":
			g, err = parseTencentUploadGrant(o, env.StartedAt)
		}
		if err != nil || g.Ref == "" {
			return nil, nil
		}
		ref, err = recoverFiveGrant(ctx, m, q.MaterialOperationKey(), g, env.CollectedAt, q.Channel == "huawei")
		success = err == nil
	case "upload":
		if q.Channel == "vivo" {
			file, e := parseVivoUploadedFile(o, t.PackageName, t.APK)
			if e != nil || file.VersionCode != t.VersionCode || file.VersionName != t.VersionName {
				return nil, nil
			}
			ref, err = recoverFiveMaterial(ctx, m, q.OperationID, struct {
				SerialNumber, PackageName, VersionName, MD5 string
				VersionCode                                 int64
			}{file.SerialNumber, file.PackageName, file.VersionName, file.MD5, file.VersionCode})
			success = err == nil
			break
		}
		key, e := materialCheckpoint(q, "authorize")
		if e != nil {
			return nil, e
		}
		g, e := loadGrant(ctx, m, key)
		if e != nil {
			return nil, e
		}
		if q.Channel == "honor" && check(o, "code") != nil {
			return nil, nil
		}
		if q.Channel != "honor" && q.Channel != "huawei" && q.Channel != "tencent" {
			return nil, nil
		}
		ref, success = g.Ref, g.Ref != ""
	case "bind":
		key, e := materialCheckpoint(q, "authorize")
		if e != nil {
			return nil, e
		}
		g, e := loadGrant(ctx, m, key)
		if e != nil {
			return nil, e
		}
		if q.Checkpoints["upload"] != g.Ref {
			return nil, fail("checkpoint_mismatch", "not_sent")
		}
		if q.Channel == "huawei" {
			if check(o.obj("ret"), "code") != nil {
				return nil, nil
			}
			g.ReadyAt = env.CollectedAt.Add(2 * time.Minute)
			ref, err = recoverFiveGrant(ctx, m, q.OperationID, g, env.CollectedAt, true)
		} else if q.Channel == "honor" && check(o, "code") == nil {
			ref = g.Ref
		} else {
			return nil, nil
		}
		success = err == nil
	case "metadata":
		if q.Checkpoints["bind"] == "" {
			return nil, fail("checkpoint_missing", "not_sent")
		}
		if q.Channel == "huawei" {
			success = check(o.obj("ret"), "code") == nil
		} else if q.Channel == "honor" {
			success = check(o, "code") == nil
		}
		ref = ack(q.OperationID).Ref
	case "push":
		success = q.Channel == "xiaomi" && check(o, "result") == nil
		ref = ack(q.OperationID).Ref
	case "submit":
		switch q.Channel {
		case "huawei":
			success = check(o.obj("ret"), "code") == nil
			ref = ack(q.OperationID).Ref
		case "honor":
			success = check(o, "code") == nil && o.str("data") != ""
			ref = o.str("data")
		case "tencent":
			success = check(o, "ret") == nil
			ref = ack(q.OperationID).Ref
		case "vivo":
			success = vivoCheck(o) == nil
			ref = ack(q.OperationID).Ref
		}
	}
	if err != nil {
		return nil, err
	}
	if !success || ref == "" {
		return nil, nil
	}
	receipt := protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: ref}
	if _, err = recoverFiveMaterial(ctx, m, fiveReceiptKey(q.MaterialOperationKey()), fiveVerifiedReceipt{q, receipt}); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func recoverFiveMaterial(ctx context.Context, m Materials, key string, value any) (string, error) {
	ref := "material-" + account.Key(key)
	secret, err := m.Get(ctx, ref)
	if errors.Is(err, credentials.ErrMissing) {
		return saveMaterial(ctx, m, key, value)
	}
	if err != nil {
		return "", credentials.PublicError(err)
	}
	defer secret.Destroy()
	b := secret.Bytes()
	defer clear(b)
	// Both values use the same private deterministic encoder. Comparing exact
	// bytes also keeps large integer identities out of floating-point parsing.
	if !bytes.Equal(b, raw(value)) {
		return "", fail("production_checkpoint_conflict", "not_sent")
	}
	return ref, nil
}

func recoverFiveGrant(ctx context.Context, m Materials, key string, g UploadGrant, collected time.Time, huawei bool) (string, error) {
	ref := "material-" + account.Key(key)
	secret, err := m.Get(ctx, ref)
	if errors.Is(err, credentials.ErrMissing) {
		return saveGrant(ctx, m, key, g)
	}
	if err != nil {
		return "", credentials.PublicError(err)
	}
	defer secret.Destroy()
	b := secret.Bytes()
	defer clear(b)
	var prior grantPayload
	if json.Unmarshal(b, &prior) != nil || prior.URL != g.URL || prior.Ref != g.Ref || prior.Method != g.Method || !reflect.DeepEqual(prior.Headers, g.Headers) {
		return "", fail("production_checkpoint_conflict", "not_sent")
	}
	// Huawei's request/parse clocks can be milliseconds apart; an already
	// stored grant may have the later conservative bind-processing deadline.
	if huawei {
		if prior.ExpiresAt.Before(g.ExpiresAt) || prior.ExpiresAt.After(collected.Add(4*time.Minute)) || prior.ReadyAt.Before(g.ReadyAt) || g.ReadyAt.IsZero() && !prior.ReadyAt.IsZero() {
			return "", fail("production_checkpoint_conflict", "not_sent")
		}
	} else if !prior.ExpiresAt.Equal(g.ExpiresAt) || !prior.ReadyAt.Equal(g.ReadyAt) {
		return "", fail("production_checkpoint_conflict", "not_sent")
	}
	return ref, nil
}
