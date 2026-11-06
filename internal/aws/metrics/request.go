package metrics

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type formDecoder struct {
	values url.Values
	used   map[string]bool
}

func decoder(values url.Values) *formDecoder {
	return &formDecoder{values, map[string]bool{"Action": true, "Version": true}}
}
func (d *formDecoder) field(name string, required bool) (string, bool, error) {
	v, ok := d.values[name]
	if !ok {
		if required {
			return "", false, missing("A required metric parameter is missing.")
		}
		return "", false, nil
	}
	if len(v) != 1 {
		return "", true, invalid("Repeated parameters are unsupported.")
	}
	d.used[name] = true
	return v[0], true, nil
}
func (d *formDecoder) indexes(prefix string, maximum int) ([]int, error) {
	set := map[int]bool{}
	marker := prefix + ".member."
	for key := range d.values {
		if !strings.HasPrefix(key, marker) {
			continue
		}
		tail := strings.TrimPrefix(key, marker)
		part := strings.SplitN(tail, ".", 2)[0]
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > maximum || strconv.Itoa(n) != part {
			return nil, invalid("List indexes must be contiguous positive integers.")
		}
		set[n] = true
	}
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	for i, n := range out {
		if n != i+1 {
			return nil, invalid("List indexes must be contiguous positive integers.")
		}
	}
	return out, nil
}
func (d *formDecoder) done() error {
	for key := range d.values {
		if !d.used[key] {
			return unsupported("An unsupported metric parameter was supplied.")
		}
	}
	return nil
}
func (d *formDecoder) dimensions(prefix string, maximum int) ([]Dimension, error) {
	indexes, err := d.indexes(prefix, maximum)
	if err != nil {
		return nil, err
	}
	out := make([]Dimension, 0, len(indexes))
	for _, n := range indexes {
		base := prefix + ".member." + strconv.Itoa(n)
		name, _, e := d.field(base+".Name", true)
		if e != nil {
			return nil, e
		}
		value, _, e := d.field(base+".Value", true)
		if e != nil {
			return nil, e
		}
		out = append(out, Dimension{name, value})
	}
	return out, nil
}
func decodePut(values url.Values) (PutInput, error) {
	d := decoder(values)
	var in PutInput
	var err error
	in.Namespace, _, err = d.field("Namespace", true)
	if err != nil {
		return in, err
	}
	indexes, err := d.indexes("MetricData", 1000)
	if err != nil {
		return in, err
	}
	if len(indexes) == 0 {
		return in, missing("MetricData is required.")
	}
	for _, n := range indexes {
		base := "MetricData.member." + strconv.Itoa(n)
		var datum Datum
		datum.MetricName, _, err = d.field(base+".MetricName", true)
		if err != nil {
			return in, err
		}
		raw, _, e := d.field(base+".Value", true)
		if e != nil {
			return in, e
		}
		datum.Value, e = strconv.ParseFloat(raw, 64)
		if e != nil {
			return in, invalid("A raw metric Value must be numeric.")
		}
		datum.Dimensions, e = d.dimensions(base+".Dimensions", 30)
		if e != nil {
			return in, e
		}
		if value, ok, e := d.field(base+".Unit", false); e != nil {
			return in, e
		} else if ok {
			datum.Unit = &value
		}
		if value, ok, e := d.field(base+".Timestamp", false); e != nil {
			return in, e
		} else if ok {
			stamp, e := time.Parse(time.RFC3339Nano, value)
			if e != nil {
				return in, invalid("Timestamp must be an ISO8601 datetime.")
			}
			datum.Timestamp = &stamp
		}
		if value, ok, e := d.field(base+".StorageResolution", false); e != nil {
			return in, e
		} else if ok {
			resolution, e := strconv.Atoi(value)
			if e != nil {
				return in, invalid("StorageResolution must be an integer.")
			}
			datum.StorageResolution = &resolution
		}
		in.Data = append(in.Data, datum)
	}
	return in, d.done()
}
func decodeList(values url.Values) (ListInput, error) {
	d := decoder(values)
	var in ListInput
	if value, ok, err := d.field("Namespace", false); err != nil {
		return in, err
	} else if ok {
		in.Namespace = &value
	}
	if value, ok, err := d.field("MetricName", false); err != nil {
		return in, err
	} else if ok {
		in.MetricName = &value
	}
	var err error
	in.NextToken, _, err = d.field("NextToken", false)
	if err != nil {
		return in, err
	}
	indexes, err := d.indexes("Dimensions", 10)
	if err != nil {
		return in, err
	}
	for _, n := range indexes {
		base := "Dimensions.member." + strconv.Itoa(n)
		name, _, err := d.field(base+".Name", true)
		if err != nil {
			return in, err
		}
		filter := DimensionFilter{Name: name}
		if value, ok, e := d.field(base+".Value", false); e != nil {
			return in, e
		} else if ok {
			filter.Value = &value
		}
		in.Dimensions = append(in.Dimensions, filter)
	}
	return in, d.done()
}
func decodeStatistics(values url.Values) (StatisticsInput, error) {
	d := decoder(values)
	var in StatisticsInput
	var err error
	in.Namespace, _, err = d.field("Namespace", true)
	if err != nil {
		return in, err
	}
	in.MetricName, _, err = d.field("MetricName", true)
	if err != nil {
		return in, err
	}
	for _, field := range []string{"StartTime", "EndTime"} {
		value, _, e := d.field(field, true)
		if e != nil {
			return in, e
		}
		stamp, e := time.Parse(time.RFC3339Nano, value)
		if e != nil {
			return in, invalid("Statistic times must be ISO8601 datetimes.")
		}
		if field == "StartTime" {
			in.Start = stamp
		} else {
			in.End = stamp
		}
	}
	period, _, err := d.field("Period", true)
	if err != nil {
		return in, err
	}
	in.Period, err = strconv.Atoi(period)
	if err != nil {
		return in, invalid("Period must be an integer.")
	}
	in.Dimensions, err = d.dimensions("Dimensions", 30)
	if err != nil {
		return in, err
	}
	indexes, err := d.indexes("Statistics", 5)
	if err != nil {
		return in, err
	}
	for _, n := range indexes {
		value, _, e := d.field("Statistics.member."+strconv.Itoa(n), true)
		if e != nil {
			return in, e
		}
		in.Statistics = append(in.Statistics, value)
	}
	if value, ok, e := d.field("Unit", false); e != nil {
		return in, e
	} else if ok {
		in.Unit = &value
	}
	return in, d.done()
}
