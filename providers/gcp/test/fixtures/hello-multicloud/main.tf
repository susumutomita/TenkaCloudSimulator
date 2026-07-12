terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0, < 6.0"
    }
  }
}

variable "region" {
  type    = string
  default = "asia-northeast1"
}

locals {
  service_name = "hello-multicloud"
}

resource "google_cloud_run_v2_service" "hello" {
  name     = local.service_name
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

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
  location = var.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "GcpHelloUrl" {
  value = google_cloud_run_v2_service.hello.uri
}
