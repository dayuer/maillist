// The cost calculator: what your list and sending schedule would cost on Nibwire, which plan that
// is, and how it compares with what you pay today. Prices are the founding prices on this page;
// keep the two in step.
(function () {
  "use strict";

  var PLANS = [
    { name: "Starter", base: 29, included: 50000 },
    { name: "Growth", base: 99, included: 250000 },
    { name: "Scale", base: 299, included: 1000000 }
  ];
  var EXTRA_PER_1K = 0.5; // every plan, beyond what it includes

  var STOPS = [1000, 2500, 5000, 10000, 25000, 50000, 75000, 100000];

  var form = document.getElementById("calc");
  if (!form) return;
  var spendIn = form.querySelector("#spend");
  var range = form.querySelector("#subs");
  var $ = function (id) { return document.getElementById(id); };

  var usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
  var count = new Intl.NumberFormat("en-US");

  // Set a figure and replay the tick animation when it changes, so every input visibly moves
  // something.
  function show(id, text) {
    var el = $(id);
    if (el.textContent === text) return;
    el.textContent = text;
    el.classList.remove("tick");
    void el.offsetWidth; // restart the animation
    el.classList.add("tick");
  }

  function round100(n) { return Math.round(n / 100) * 100; }

  // What a month of `emails` costs on one plan.
  function priced(plan, emails) {
    var extra = Math.max(0, emails - plan.included);
    var extraCost = Math.ceil(extra / 1000 * EXTRA_PER_1K);
    return { plan: plan, extra: extra, extraCost: extraCost, total: plan.base + extraCost };
  }

  // The plan that costs least for this volume.
  function bestFor(emails) {
    return PLANS.map(function (p) { return priced(p, emails); })
      .reduce(function (a, b) { return b.total < a.total ? b : a; });
  }

  // The most emails `spend` buys on any plan.
  function budgetEmails(spend) {
    return PLANS.reduce(function (most, p) {
      if (spend < p.base) return most;
      return Math.max(most, p.included + (spend - p.base) / EXTRA_PER_1K * 1000);
    }, 0);
  }

  function update() {
    var spend = Math.max(0, Math.min(100000, Number(spendIn.value) || 0));
    var i = Number(range.value);
    var subs = STOPS[i];
    var freq = Number(form.querySelector("#freq").value);

    range.style.setProperty("--fill", (i / (STOPS.length - 1)) * 100 + "%");
    range.setAttribute("aria-valuetext", count.format(subs) + " subscribers");
    $("subs-out").textContent = count.format(subs);

    // 1. What today's budget would buy.
    var budget = budgetEmails(spend);
    $("spend-echo").textContent = usd.format(spend);
    show("budget-out", budget > 0 ? count.format(round100(budget)) : "0");

    // 2. What you actually send, and what that costs.
    var need = round100(subs * freq);
    var q = bestFor(need);
    show("need-out", count.format(need));
    show("plan-name", q.plan.name);
    $("plan-incl").textContent = "includes " + count.format(q.plan.included);
    show("plan-base", usd.format(q.plan.base));
    $("over-note").textContent = q.extra > 0
      ? count.format(q.extra) + " × $0.50 / 1,000"
      : "none";
    show("over-cost", usd.format(q.extraCost));
    show("total-out", usd.format(q.total) + "/mo");

    // 3. Against today's bill.
    var diff = spend - q.total;
    var over = diff < 0;
    $("save").classList.toggle("over", over);
    $("save-label").textContent = over ? "More than today, per month" : "You keep, per year";
    show("save-amt", over ? usd.format(-diff) + "/mo" : usd.format(diff * 12));
  }

  form.addEventListener("input", update);
  form.addEventListener("change", update);
  form.addEventListener("submit", function (e) { e.preventDefault(); });
  update();
})();
