select p.name, sum(i.quantity)::int as sold
from product p join order_item i on i.product_id = p.id
group by p.name
order by sold desc, p.name asc
limit 3