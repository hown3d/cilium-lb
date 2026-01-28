import http from "k6/http";
import exec from 'k6/execution';
import { check, sleep } from "k6";

const BASE_URL = __ENV.BASE_URL || 'http://188.34.73.103:9898';

export const options = {
  scenarios: {
    test: {
      executor: 'shared-iterations',
      vus: 100,
      iterations: 1000
    }
  }
};

export function setup() {
  let res = http.get(BASE_URL);
  if (res.status !== 200) {
    exec.test.abort(`Got unexpected status code ${res.status} when trying to setup. Exiting.`);
  }
}

export default function () {
  let payload = {
    foo: "bar",
  };

  let res = http.post(BASE_URL + "/api/echo", JSON.stringify(payload));

  check(res, { "status is 202": (res) => res.status === 202 });
  console.log(res.json());
}
