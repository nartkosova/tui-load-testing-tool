import http from 'k6/http';

export const options = {
  vus: Number(__ENV.VUS),
  duration: __ENV.DURATION,
  discardResponseBodies: true,
  summaryTrendStats: ['min', 'avg', 'med', 'p(95)', 'p(99)', 'max', 'count'],
  thresholds: {
    'http_reqs{status:0}': ['count>=0'],
    'http_reqs{expected_response:false}': ['count>=0'],
    'http_req_duration{expected_response:true}': ['max>=0'],
  },
};

const params = { timeout: __ENV.TIMEOUT };

export default function () {
  http.get(__ENV.URL, params);
}

export function handleSummary(data) {
  return { [__ENV.SUMMARY]: JSON.stringify(data) };
}
