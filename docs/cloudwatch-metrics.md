# CloudWatch custom metric statistics

The simulator accepts `PutMetricData` and `GetMetricStatistics` at the AWS root and its standard local alias `/_simulator/providers/aws/` using the 2010-08-01 Query protocol. The pinned boto3 CloudWatch client uses the signing name `monitoring`. Use the simulator's numeric loopback URL and local access token. The two IAM actions are `cloudwatch:PutMetricData` and `cloudwatch:GetMetricStatistics`, each with resource `*`. A participant's selected identity and saved policy control access. A dummy SDK signing key selects the destination. The local IAM subset rejects policy conditions such as `cloudwatch:namespace`. [AWS service authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazoncloudwatch.html)

A successful write appends every raw observation to the team's SQLite database in one transaction. Sending the same datum twice contributes two samples. A metric is identified by region, custom namespace, name and the complete set of dimension name/value pairs. Units form separate series for aggregation. The region must be one of the simulator's supported AWS regions.

| Input | Accepted subset |
| --- | --- |
| Request | Form-encoded POST to `/`, exact `Version=2010-08-01`. Put also accepts one gzip member. Both the wire and expanded body have a 1 MiB local limit and the form accepts at most 10,000 fields. |
| Batch | 1–1,000 individual numeric `Value` samples. Values must be finite and between −2³⁶⁰ and +2³⁶⁰ inclusive. |
| Dimension | Up to 30 distinct names. Printable ASCII names are 1–255 bytes and values are 1–1,024 bytes. Both require a non-space character. Names cannot begin with a colon. |
| Namespace and name | A printable custom namespace of up to 255 ASCII characters outside `AWS/` and without a leading colon. Metric names accept up to 255 Unicode characters without control characters. Both require a non-space character. |
| Timestamp | RFC 3339. Each write uses one server time for omitted timestamps and accepts times from 14 days before that time through two hours after it. |
| Resolution and unit | Standard 60-second resolution. An omitted unit becomes `None`. The native standard unit names are accepted. |

A statistics query rounds its start down to the nearest UTC minute, includes that rounded start and excludes its end. Supply a positive period in seconds that is a multiple of 60. The simulator groups observations into periods starting at that rounded start and returns only buckets containing data. Choose one or more of `SampleCount`, `Sum`, `Average`, `Minimum` and `Maximum`. Average uses all raw samples in the period. Pass a unit to select it or omit the unit to receive separate unit/time datapoints. The local limit is 1,440 requested time buckets and 1,440 returned datapoints across all units. [AWS PutMetricData](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_PutMetricData.html) · [AWS GetMetricStatistics](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_GetMetricStatistics.html)

Raw samples are queryable for 15 days. An authorized successful write prunes older rows in the same region and namespace. Queries, denied requests and failed batches preserve stored rows. Local limits are 10,000 stored samples per region/namespace and 100,000 stored samples per team database. Expired rows in other namespaces still occupy the team quota until a successful write prunes that scope. Teams can dispose of their own simulator database to clean up a practice run.

The supported entry points are the Query SDK and HTTP API. ListMetrics, GetMetricData, alarms, dashboards, Insights, metric math, percentile and statistic-set ingestion, Values/Counts arrays, entities, high resolution, automatic collection and long-term rollups are outside this subset. CloudWatch Logs uses its own API and storage. [CloudWatch Logs](cloudwatch-logs.md)
