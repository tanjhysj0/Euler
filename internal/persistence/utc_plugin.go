package persistence

import (
	"errors"
	"reflect"
	"time"

	"gorm.io/gorm"
)

// utcNormalizer 是一个 GORM 插件：在 create / update 执行前，把所有
// time.Time 与 *time.Time 字段的值规范化为 UTC。
//
// 背景：SQLite 没有独立的时间类型，时间以文本存储并按文本排序。若写入时
// 保留了本地时区偏移（如 +08:00），不同偏移的字符串字典序会与真实时间序
// 不一致（例如 "2026-11-27T01:00:00+08:00" 会排在 "2026-11-26T18:00:00Z"
// 之后，尽管前者在时刻上更早）。统一以 UTC 写入后，文本序即与时间序一致，
// 与 PostgreSQL 的 timestamptz（按瞬间比较）行为对齐。
type utcNormalizer struct{}

// Name 返回插件名称。
func (utcNormalizer) Name() string { return "utc_normalizer" }

// Initialize 把规范化回调注册到 create 与 update 流程。
func (utcNormalizer) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	return errors.Join(
		cb.Create().Before("gorm:create").Register("utc_normalizer:create", normalizeUTCTimes),
		cb.Update().Before("gorm:update").Register("utc_normalizer:update", normalizeUTCTimes),
	)
}

var timeType = reflect.TypeOf(time.Time{})

// normalizeUTCTimes 是 GORM 回调入口：规范化 Dest 中的时间字段。
func normalizeUTCTimes(db *gorm.DB) {
	if db == nil || db.Statement == nil || db.Statement.Dest == nil {
		return
	}
	normalizeReflectValue(reflect.ValueOf(db.Statement.Dest))
}

// normalizeReflectValue 递归遍历 v，把其中的时间值改为 UTC。
func normalizeReflectValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() { //nolint:exhaustive // reflect.Kind 是标准库枚举，只需处理容器类型，其余由 default 兜底
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return
		}
		normalizeReflectValue(v.Elem())
	case reflect.Struct:
		normalizeStruct(v)
	case reflect.Slice, reflect.Array:
		normalizeSlice(v)
	case reflect.Map:
		normalizeMap(v)
	default:
	}
}

// normalizeStruct 遍历结构体字段，递归规范化可设置的时间字段。
func normalizeStruct(v reflect.Value) {
	if v.Type() == timeType {
		setUTCTime(v)
		return
	}
	for i := range v.NumField() {
		field := v.Field(i)
		if field.CanSet() || (field.Kind() == reflect.Ptr && !field.IsNil()) {
			normalizeReflectValue(field)
		}
	}
}

// normalizeSlice 遍历切片 / 数组元素，递归规范化。
func normalizeSlice(v reflect.Value) {
	for i := range v.Len() {
		normalizeReflectValue(v.Index(i))
	}
}

// setUTCTime 把一个可寻址的 time.Time 值设为其 UTC 等价时刻。
func setUTCTime(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	t, ok := v.Interface().(time.Time)
	if !ok {
		return
	}
	v.Set(reflect.ValueOf(t.UTC()))
}

// normalizeMap 处理 Updates(map[string]any{...}) 场景：把 map 中
// 的 time.Time / *time.Time 值替换为 UTC 等价物。
func normalizeMap(v reflect.Value) {
	iter := v.MapRange()
	for iter.Next() {
		mv := iter.Value()
		if mv.Kind() == reflect.Interface {
			mv = mv.Elem()
		}
		switch {
		case isTimeValue(mv):
			t, ok := mv.Interface().(time.Time)
			if !ok {
				continue
			}
			v.SetMapIndex(iter.Key(), reflect.ValueOf(t.UTC()))
		case isTimePtr(mv):
			t, ok := mv.Elem().Interface().(time.Time)
			if !ok {
				continue
			}
			utc := t.UTC()
			v.SetMapIndex(iter.Key(), reflect.ValueOf(&utc))
		default:
		}
	}
}

// isTimeValue 判断 v 是否是一个有效的 time.Time 值。
func isTimeValue(v reflect.Value) bool {
	return v.IsValid() && v.Type() == timeType
}

// isTimePtr 判断 v 是否是一个非空的 *time.Time 值。
func isTimePtr(v reflect.Value) bool {
	return v.IsValid() && v.Kind() == reflect.Ptr && !v.IsNil() &&
		v.Type().Elem() == timeType
}
