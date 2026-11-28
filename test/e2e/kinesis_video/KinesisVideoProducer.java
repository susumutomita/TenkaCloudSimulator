// Actual published Java clients for a finite, local Kinesis Video upload.
import com.amazonaws.AmazonServiceException;
import com.amazonaws.auth.AWSStaticCredentialsProvider;
import com.amazonaws.auth.BasicAWSCredentials;
import com.amazonaws.kinesisvideo.client.PutMediaClient;
import com.amazonaws.kinesisvideo.client.signing.KinesisVideoAWS4Signer;
import com.amazonaws.kinesisvideo.config.ClientConfiguration;
import com.amazonaws.services.kinesisvideo.AmazonKinesisVideoPutMediaClient;
import com.amazonaws.services.kinesisvideo.PutMediaAckResponseHandler;
import com.amazonaws.services.kinesisvideo.model.AckEvent;
import com.amazonaws.services.kinesisvideo.model.PutMediaRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.apache.http.impl.io.ChunkedInputStream;
import org.apache.http.impl.io.DefaultHttpResponseParser;
import org.apache.http.impl.io.HttpTransportMetricsImpl;
import org.apache.http.impl.io.SessionInputBufferImpl;

import java.io.BufferedReader;
import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

public final class KinesisVideoProducer {
    private final ObjectMapper json = new ObjectMapper();
    private final CountDownLatch complete = new CountDownLatch(1);
    private final List<Receipt> events = new ArrayList<>();
    private int status;
    private Throwable failure;

    private record Receipt(String event, String number, long timecode) {}

    // The Producer SDK delivers the actual HTTP stream to its receive callback.
    private void receive(InputStream wire) {
        try {
            var input = new SessionInputBufferImpl(new HttpTransportMetricsImpl(), 8192);
            input.bind(wire);
            var response = new DefaultHttpResponseParser(input).parse();
            status = response.getStatusLine().getStatusCode();
            if (status != 200) return;
            var transfer = response.getFirstHeader("Transfer-Encoding");
            if (transfer == null || !transfer.getValue().equalsIgnoreCase("chunked")) {
                throw new IOException("Expected a chunked acknowledgment response");
            }
            var body = new ChunkedInputStream(input).readNBytes(16385);
            if (body.length == 0 || body.length > 16384 || body[body.length - 1] != '\n') {
                throw new IOException("Invalid finite acknowledgment response");
            }
            for (String line : new String(body, StandardCharsets.UTF_8).split("\n")) {
                var event = json.readTree(line);
                if (!event.path("EventType").isTextual() || !event.path("FragmentTimecode").isIntegralNumber()) {
                    throw new IOException("Missing acknowledgment fields");
                }
                events.add(new Receipt(event.get("EventType").textValue(), event.path("FragmentNumber").textValue(),
                                       event.get("FragmentTimecode").longValue()));
            }
        } catch (IOException | org.apache.http.HttpException e) {
            throw new IllegalStateException("Unable to read SDK acknowledgment", e);
        }
    }

    private void await() throws InterruptedException {
        if (!complete.await(15, TimeUnit.SECONDS)) throw new IllegalStateException("No SDK completion callback");
    }

    private void upload(String mode, URI origin, String name, byte[] media, long millis, String token) throws Exception {
        var credentials = new AWSStaticCredentialsProvider(new BasicAWSCredentials("TCSIMCATALOG", "dummy-local-only"));
        if (mode.equals("netty")) {
            try (var client = AmazonKinesisVideoPutMediaClient.builder().withRegion("us-east-1").withEndpoint(origin)
                    .withCredentials(credentials).withConnectionTimeoutInMillis(5000).withNumberOfThreads(1).build()) {
                var request = new PutMediaRequest().withStreamName(name).withProducerStartTimestamp(new Date(millis))
                    .withFragmentTimecodeType("RELATIVE").withPayload(new ByteArrayInputStream(media));
                // This public property is lost by the 1.12.797 special Netty transport.
                request.putCustomRequestHeader("X-Simulator-Token", token);
                client.putMedia(request, new PutMediaAckResponseHandler() {
                    public void onAckEvent(AckEvent item) {
                        events.add(new Receipt(item.getAckEventType().getRawValue(), item.getFragmentNumber(), item.getFragmentTimecode()));
                    }
                    public void onFailure(Throwable error) {
                        if (error instanceof AmazonServiceException service) status = service.getStatusCode();
                        else failure = error;
                        complete.countDown();
                    }
                    public void onComplete() {
                        status = 200;
                        complete.countDown();
                    }
                });
                await();
            }
        } else {
            if (!mode.equals("producer") && !mode.equals("producer-iso")) throw new IllegalArgumentException("Unknown producer");
            var route = origin.resolve("/putMedia");
            var config = ClientConfiguration.builder().region("us-east-1").serviceName("kinesisvideo").streamName(name).streamUri(route).build();
            var signer = new KinesisVideoAWS4Signer(credentials, config);
            var producer = PutMediaClient.builder().putMediaDestinationUri(route).streamName(name)
                .mkvStream(new ByteArrayInputStream(media)).fragmentTimecodeType("RELATIVE").timestamp(millis)
                .signWith(http -> {
                    if (mode.equals("producer-iso")) {
                        // Exercise the timestamp syntax independently observed on the standard Netty client.
                        http.getHeaders().put("x-amzn-producer-start-timestamp", Instant.ofEpochMilli(millis).toString());
                    }
                    http.getHeaders().put("X-Simulator-Token", token);
                    signer.sign(http);
                }).receiveAcks(this::receive).receiveTimeout(8000)
                .receiveCompletion(error -> { failure = error; complete.countDown(); }).build();
            producer.putMediaInBackground();
            try { await(); } finally { producer.close(); }
        }
        if (failure != null || status == 0) throw new IllegalStateException("Incomplete SDK request");
        System.out.println("KINESIS_FIXTURE " + json.writeValueAsString(Map.of("status", status, "events", events)));
    }

    public static void main(String[] args) throws Exception {
        URI origin = URI.create(args[1]);
        if (!"http".equals(origin.getScheme()) || !"127.0.0.1".equals(origin.getHost()) || origin.getPort() < 1 ||
            !origin.getRawPath().isEmpty() || origin.getRawQuery() != null || origin.getUserInfo() != null || origin.getRawFragment() != null) {
            throw new IllegalArgumentException("A local simulator origin is required");
        }
        String token = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8)).readLine();
        if (token == null || !token.matches("[A-Za-z0-9_.=:-]{1,256}")) throw new IllegalArgumentException("Missing local token");
        Path file = Path.of(args[3]);
        if (Files.size(file) < 1 || Files.size(file) > 131072) throw new IllegalArgumentException("Invalid fixture size");
        new KinesisVideoProducer().upload(args[0], origin, args[2], Files.readAllBytes(file), Long.parseLong(args[4]), token);
    }
}
