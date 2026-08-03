<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req) {
    $ms = 5000;
    if (isset($req['query']) && preg_match('/delay=(\d+)/', (string) $req['query'], $m)) {
        $ms = (int) $m[1];
    }
    usleep($ms * 1000);
    return eregion_ok_response($req, 'slow');
});
