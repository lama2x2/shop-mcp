-- Reference queries for the 8 questions from the assignment.
-- Run: sqlite3 -readonly shop.db < testdata/golden.sql
-- Results are frozen in testdata/golden.json and used by integration tests.
--
-- Revenue rule (default): SUM(order_items.quantity * order_items.unit_price)
-- over orders whose status <> 'cancelled'. For this dataset it equals
-- SUM(orders.total_amount) over the same orders (verified: 0 mismatches).

.headers on
.mode column

-- Q1. Tables and row counts
SELECT 'customers' AS tbl, COUNT(*) AS row_count FROM customers
UNION ALL SELECT 'products', COUNT(*) FROM products
UNION ALL SELECT 'orders', COUNT(*) FROM orders
UNION ALL SELECT 'order_items', COUNT(*) FROM order_items;

-- Q2 / Q3. There is no country column. Evidence for the answer:
SELECT name FROM pragma_table_info('customers');
SELECT substr(phone, 1, 2) AS phone_prefix, COUNT(*) AS customers
FROM customers GROUP BY 1;

-- Q4. Customer who spent the most
SELECT c.id, c.first_name, c.last_name, c.email,
       ROUND(SUM(oi.quantity * oi.unit_price), 2) AS total_spent,
       COUNT(DISTINCT o.id) AS order_count
FROM customers c
JOIN orders o       ON o.customer_id = c.id
JOIN order_items oi ON oi.order_id = o.id
WHERE o.status <> 'cancelled'
GROUP BY c.id
ORDER BY total_spent DESC, order_count DESC, c.id
LIMIT 3;

-- Q5. Top 5 products by units sold (tie-break: revenue DESC, id ASC)
SELECT p.id, p.name, p.category,
       SUM(oi.quantity) AS units_sold,
       ROUND(SUM(oi.quantity * oi.unit_price), 2) AS revenue
FROM order_items oi
JOIN orders o   ON o.id = oi.order_id
JOIN products p ON p.id = oi.product_id
WHERE o.status <> 'cancelled'
GROUP BY p.id
ORDER BY units_sold DESC, revenue DESC, p.id
LIMIT 7;

-- Q6. Top 3 categories by revenue
SELECT p.category,
       ROUND(SUM(oi.quantity * oi.unit_price), 2) AS revenue,
       SUM(oi.quantity) AS units_sold
FROM order_items oi
JOIN orders o   ON o.id = oi.order_id
JOIN products p ON p.id = oi.product_id
WHERE o.status <> 'cancelled'
GROUP BY p.category
ORDER BY revenue DESC
LIMIT 3;

-- Q7. Revenue in 2025 (expected: no orders) + data range for context
SELECT COUNT(DISTINCT o.id) AS order_count,
       ROUND(COALESCE(SUM(oi.quantity * oi.unit_price), 0), 2) AS revenue
FROM orders o
JOIN order_items oi ON oi.order_id = o.id
WHERE o.status <> 'cancelled'
  AND o.order_date >= '2025-01-01' AND o.order_date < '2026-01-01';
SELECT MIN(order_date) AS first_order, MAX(order_date) AS last_order FROM orders;

-- Q8. Customer with the most orders
SELECT c.id, c.first_name, c.last_name, c.email, COUNT(*) AS order_count
FROM customers c
JOIN orders o ON o.customer_id = c.id
WHERE o.status <> 'cancelled'
GROUP BY c.id
ORDER BY order_count DESC, c.id
LIMIT 3;
