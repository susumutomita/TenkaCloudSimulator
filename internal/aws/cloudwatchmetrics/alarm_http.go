package cloudwatchmetrics

import (
	"encoding/xml"
	"net/http"
)

func (s *Store) serveAlarm(w http.ResponseWriter, r *http.Request, region, action string, fields form, authorize func(string, string) (bool, error)) {
	var c alarmConfig
	var q alarmDescribe
	var name string
	var err error
	switch action {
	case "PutMetricAlarm":
		c, err = fields.alarmConfig()
		name = c.AlarmName
	case "DescribeAlarms":
		q, err = fields.alarmDescribe()
		name = q.name
	case "DeleteAlarms":
		name, err = fields.alarmName("AlarmNames.member.1")
		if err == nil && len(fields) != 0 {
			err = invalid("Delete exactly one named alarm; unsupported fields are refused.")
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	resource := "*"
	if name != "" {
		resource = alarmARN(region, name)
	}
	allowed, err := authorize("cloudwatch:"+action, resource)
	if err != nil {
		writeError(w, err)
		return
	}
	if !allowed {
		writeError(w, requestError{"AccessDenied", "CloudWatch alarm action is not allowed.", 403})
		return
	}
	now := s.now().UTC()
	switch action {
	case "PutMetricAlarm":
		err = s.saveAlarm(r.Context(), region, c, now)
	case "DeleteAlarms":
		err = s.deleteAlarm(r.Context(), region, name)
	case "DescribeAlarms":
		var alarms []metricAlarm
		var token string
		alarms, token, err = s.describeAlarms(r.Context(), region, q, now)
		if err == nil {
			result := struct {
				XMLName   xml.Name
				Alarms    []metricAlarm `xml:"MetricAlarms>member"`
				NextToken string        `xml:",omitempty"`
			}{XMLName: xml.Name{Local: "DescribeAlarmsResult"}, Alarms: alarms, NextToken: token}
			writeXML(w, 200, alarmXML(action, result))
			return
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeXML(w, 200, alarmXML(action, nil))
}
