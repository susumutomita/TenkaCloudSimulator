resource "google_cloud_run_v2_service" "hello" {
  name     = "cli-hello"
  location = "asia-northeast1"
  template {
    scaling {
      max_instance_count = 1
    }
    containers {
      image = "gcr.io/cloudrun/hello"
    }
  }
}

resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  name     = google_cloud_run_v2_service.hello.name
  location = "asia-northeast1"
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "GcpHelloUrl" {
  value = google_cloud_run_v2_service.hello.uri
}
