# Local Kinesis Video Streams media

Create a stream with the local default key and a positive `DataRetentionInHours` to keep uploaded fragments. `GetDataEndpoint` accepts one stream name or ARN and either `PUT_MEDIA` or `GET_MEDIA`. Its response contains the actual numeric-loopback origin. After restarting the simulator, discover the endpoint again.

| Operation | Local behavior |
| --- | --- |
| `PutMedia` | POST a finite Matroska segment with relative timecodes. The response is a stream of newline-delimited acknowledgements. A retained upload receives `BUFFERING`, `RECEIVED` and `PERSISTED` with a fragment number. A zero-retention upload receives the first two acknowledgements. |
| `GetMedia` | POST an `EARLIEST` selector to read a finite snapshot of retained Matroska segments. The returned segments include fragment number, producer/server timestamps, lag from the newest fragment and a continuation token. Use `CONTINUATION_TOKEN` with that token to read subsequent fragments. An empty snapshot has an empty body. |

Each operation checks its own IAM action against the selected stream ARN. Reading after restart uses the same team database. Deleting or recreating a stream invalidates its former ARN and cursors. Shortening retention removes expired data, and increasing retention applies to the fragments still retained. The simulator stores bytes in SQLite. Custom KMS key settings, WebRTC signaling, HLS and archived-media APIs are outside this local workflow.

The upload profile allows one AVC video track, one cluster, a millisecond timestamp scale and at most 512 visible, unlaced frames spanning up to ten seconds. The first marked keyframe must contain an IDR slice. The implementation preserves codec-private data and encoded frame payloads. Each upload is limited to 4 MiB. A stream can retain 16 fragments and 32 MiB, with limits of 128 fragments and 64 MiB per team database.

For Java ingestion, the AWS Kinesis Video Streams Producer SDK supports a signing callback that can add the simulator's local authentication header before calling the stock signer. The separate AWS Java SDK v1 Netty `PutMedia` client drops custom request headers on its ordinary request path. Use an authenticated producer and a separately authorized reader. [Producer SDK](https://github.com/awslabs/amazon-kinesis-video-streams-producer-sdk-java) · [PutMedia](https://docs.aws.amazon.com/kinesisvideostreams/latest/APIReference/API_dataplane_PutMedia.html) · [GetMedia](https://docs.aws.amazon.com/kinesisvideostreams/latest/APIReference/API_dataplane_GetMedia.html)

## Run the media checks

The client checks use a Java 17 or newer JDK, FFmpeg and FFprobe, the Python packages in `scripts/requirements-test.txt`, full Playwright Chromium and the repository's pinned axe-core script. Prepare the checksum-pinned Java producer dependencies before running the isolated clients.

```sh
make prepare-kinesis-video-java PYTHON=/path/to/venv/bin/python
AXE_SCRIPT=/path/to/axe.min.js make e2e-kinesis-video PYTHON=/path/to/venv/bin/python
```

The default JAR directory is `dist/kinesis-video-jars`. Set `KINESIS_VIDEO_JARS` on both commands to use a different directory. The SDK exercise writes two different clips, decodes the returned frames after restarting the team and reads from the returned continuation token. The Console exercise uploads a real file and uses Scalar to discover the endpoint and download those stored fragments.
